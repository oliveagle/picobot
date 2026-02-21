package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ReportTemplate represents a DingTalk report template
type ReportTemplate struct {
	ReportCode string `json:"report_code"`
	Name       string `json:"name"`
}

// ReportContent represents a single content item in a report
type ReportContent struct {
	ContentType string `json:"content_type"` // "markdown"
	Sort        string `json:"sort"`
	Type        string `json:"type"`
	Content     string `json:"content"`
	Key         string `json:"key"`
}

// ListTemplatesResponse represents the response from listing report templates
type ListTemplatesResponse struct {
	Result struct {
		TemplateList []ReportTemplate `json:"template_list"`
	} `json:"result"`
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// CreateReportResponse represents the response from creating a report
type CreateReportResponse struct {
	Result  string `json:"result"` // Report ID
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// ListReportTemplates lists available report templates for a user
func (c *Client) ListReportTemplates(ctx context.Context, userID string) (*ListTemplatesResponse, error) {
	url := fmt.Sprintf("%s/topapi/report/template/listbyuserid", OldBaseURL)

	body := map[string]string{
		"userid": userID,
	}

	respBody, err := c.doOldAPIRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("list templates failed: %w", err)
	}

	var result ListTemplatesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// CreateReport creates a new report
func (c *Client) CreateReport(ctx context.Context, userID, templateID, title string, contents []ReportContent, toUserIDs, toChatIDs []string) (*CreateReportResponse, error) {
	url := fmt.Sprintf("%s/topapi/report/create", OldBaseURL)

	reportParam := map[string]interface{}{
		"template_id": templateID,
		"contents":    contents,
		"userid":      userID,
		"dd_from":     "report",
	}

	if len(toUserIDs) > 0 {
		reportParam["to_userids"] = toUserIDs
	}
	if len(toChatIDs) > 0 {
		reportParam["to_chat"] = true
		reportParam["to_cids"] = toChatIDs
	}

	body := map[string]interface{}{
		"create_report_param": reportParam,
	}

	respBody, err := c.doOldAPIRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("create report failed: %w", err)
	}

	var result CreateReportResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// CreateDailyReport creates a daily report
func (c *Client) CreateDailyReport(ctx context.Context, userID, templateID, workDone, planNext, thoughts string, toUserIDs, toChatIDs []string) (*CreateReportResponse, error) {
	contentParts := []string{fmt.Sprintf("**今日完成工作：**\n%s", workDone)}
	if planNext != "" {
		contentParts = append(contentParts, fmt.Sprintf("\n\n**明日工作计划：**\n%s", planNext))
	}
	if thoughts != "" {
		contentParts = append(contentParts, fmt.Sprintf("\n\n**工作感悟：**\n%s", thoughts))
	}

	fullContent := strings.Join(contentParts, "\n")

	contents := []ReportContent{{
		ContentType: "markdown",
		Sort:        "0",
		Type:        "1",
		Content:     fullContent,
		Key:         "今日完成工作",
	}}

	return c.CreateReport(ctx, userID, templateID, "日报", contents, toUserIDs, toChatIDs)
}

// CreateWeeklyReport creates a weekly report
func (c *Client) CreateWeeklyReport(ctx context.Context, userID, templateID, summary, planNext, thoughts string, toUserIDs, toChatIDs []string) (*CreateReportResponse, error) {
	contentParts := []string{fmt.Sprintf("**本周工作总结：**\n%s", summary)}
	contentParts = append(contentParts, fmt.Sprintf("\n\n**下周工作计划：**\n%s", planNext))
	if thoughts != "" {
		contentParts = append(contentParts, fmt.Sprintf("\n\n**工作感悟：**\n%s", thoughts))
	}

	fullContent := strings.Join(contentParts, "\n")

	contents := []ReportContent{{
		ContentType: "markdown",
		Sort:        "0",
		Type:        "1",
		Content:     fullContent,
		Key:         "今日完成工作",
	}}

	return c.CreateReport(ctx, userID, templateID, "周报", contents, toUserIDs, toChatIDs)
}

// Helper function for joining strings
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
