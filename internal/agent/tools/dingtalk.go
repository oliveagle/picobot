package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/local/picobot/internal/dingtalk"
)

// DingTalkTool provides access to DingTalk APIs (Todo, Report, Calendar)
type DingTalkTool struct {
	client  *dingtalk.Client
	unionID string
	userID  string
	enabled bool
}

// NewDingTalkTool creates a new DingTalk tool with the given credentials
func NewDingTalkTool(clientID, clientSecret, unionID, userID string) *DingTalkTool {
	if clientID == "" || clientSecret == "" {
		return &DingTalkTool{enabled: false}
	}

	return &DingTalkTool{
		client:  dingtalk.NewClient(clientID, clientSecret),
		unionID: unionID,
		userID:  userID,
		enabled: true,
	}
}

func (d *DingTalkTool) Name() string { return "dingtalk" }
func (d *DingTalkTool) Description() string {
	return "Manage DingTalk todos, reports, and calendar events"
}

func (d *DingTalkTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"todo_create", "todo_list", "todo_complete", "todo_delete", "report_create", "calendar_create"},
				"description": "The DingTalk action to perform",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Title for todo, report, or event",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content/description",
			},
			"due_date": map[string]interface{}{
				"type":        "string",
				"description": "Due date (YYYY-MM-DD or 'today', 'tomorrow')",
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Task ID for todo operations",
			},
			"report_type": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"daily", "weekly"},
				"description": "Type of report to create",
			},
			"plan": map[string]interface{}{
				"type":        "string",
				"description": "Plan/next steps for report",
			},
			"thoughts": map[string]interface{}{
				"type":        "string",
				"description": "Thoughts/reflections for report",
			},
			"event_date": map[string]interface{}{
				"type":        "string",
				"description": "Event date (YYYY-MM-DD)",
			},
			"event_time": map[string]interface{}{
				"type":        "string",
				"description": "Event time (HH:MM)",
			},
			"duration_minutes": map[string]interface{}{
				"type":        "number",
				"description": "Event duration in minutes (default: 60)",
			},
			"location": map[string]interface{}{},
			"status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"pending", "completed"},
				"description": "Filter todo status",
			},
		},
		"required": []string{"action"},
	}
}

func (d *DingTalkTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	if !d.enabled {
		return "", fmt.Errorf("DingTalk is not configured. Please set client_id and client_secret in config.")
	}

	action, _ := args["action"].(string)

	switch action {
	case "todo_create":
		return d.todoCreate(ctx, args)
	case "todo_list":
		return d.todoList(ctx, args)
	case "todo_complete":
		return d.todoComplete(ctx, args)
	case "todo_delete":
		return d.todoDelete(ctx, args)
	case "report_create":
		return d.reportCreate(ctx, args)
	case "calendar_create":
		return d.calendarCreate(ctx, args)
	default:
		return "", fmt.Errorf("unknown action: %s", action)
	}
}

func (d *DingTalkTool) todoCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	title, _ := args["title"].(string)
	if title == "" {
		return "", fmt.Errorf("title is required for todo_create")
	}

	content, _ := args["content"].(string)
	dueDate, _ := args["due_date"].(string)

	task := &dingtalk.TodoTask{
		Subject:     title,
		Description: content,
	}

	if dueDate != "" {
		timestamp := parseDate(dueDate)
		if timestamp > 0 {
			task.DueTime = timestamp
		}
	}

	resp, err := d.client.CreateTodo(ctx, d.unionID, task)
	if err != nil {
		return "", fmt.Errorf("create todo failed: %w", err)
	}

	return fmt.Sprintf("✅ Todo created successfully! ID: %s", resp.ID), nil
}

func (d *DingTalkTool) todoList(ctx context.Context, args map[string]interface{}) (string, error) {
	status, _ := args["status"].(string)

	var isDone *bool
	if status == "pending" {
		v := false
		isDone = &v
	} else if status == "completed" {
		v := true
		isDone = &v
	}

	resp, err := d.client.ListTodos(ctx, d.unionID, isDone, 100)
	if err != nil {
		return "", fmt.Errorf("list todos failed: %w", err)
	}

	if len(resp.TodoCards) == 0 {
		return "📋 No todos found.", nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("📋 Found %d todos:\n\n", len(resp.TodoCards)))

	for i, task := range resp.TodoCards {
		statusIcon := "⏳"
		if task.IsDone {
			statusIcon = "✅"
		}

		result.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, statusIcon, task.Subject))
		result.WriteString(fmt.Sprintf("   ID: %s\n", task.TaskID))

		if task.DueTime > 0 {
			due := time.Unix(task.DueTime/1000, 0).Format("2006-01-02")
			result.WriteString(fmt.Sprintf("   Due: %s\n", due))
		}
		result.WriteString("\n")
	}

	return result.String(), nil
}

func (d *DingTalkTool) todoComplete(ctx context.Context, args map[string]interface{}) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for todo_complete")
	}

	if err := d.client.CompleteTodo(ctx, d.unionID, taskID); err != nil {
		return "", fmt.Errorf("complete todo failed: %w", err)
	}

	return "✅ Todo marked as completed!", nil
}

func (d *DingTalkTool) todoDelete(ctx context.Context, args map[string]interface{}) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for todo_delete")
	}

	if err := d.client.DeleteTodo(ctx, d.unionID, taskID); err != nil {
		return "", fmt.Errorf("delete todo failed: %w", err)
	}

	return "🗑️ Todo deleted successfully!", nil
}

func (d *DingTalkTool) reportCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	reportType, _ := args["report_type"].(string)
	content, _ := args["content"].(string)
	plan, _ := args["plan"].(string)
	thoughts, _ := args["thoughts"].(string)

	if content == "" {
		return "", fmt.Errorf("content is required for report_create")
	}

	// Get templates first
	templatesResp, err := d.client.ListReportTemplates(ctx, d.userID)
	if err != nil {
		return "", fmt.Errorf("list templates failed: %w", err)
	}

	if templatesResp.ErrCode != 0 {
		return "", fmt.Errorf("list templates failed: %s", templatesResp.ErrMsg)
	}

	templates := templatesResp.Result.TemplateList
	if len(templates) == 0 {
		return "", fmt.Errorf("no report templates available")
	}

	// Find appropriate template (daily template)
	var templateID string
	for _, t := range templates {
		if strings.Contains(t.Name, "日报") && !strings.Contains(t.Name, "周报") && !strings.Contains(t.Name, "月报") {
			templateID = t.ReportCode
			break
		}
	}
	if templateID == "" {
		templateID = templates[0].ReportCode
	}

	var resp *dingtalk.CreateReportResponse
	if reportType == "weekly" {
		resp, err = d.client.CreateWeeklyReport(ctx, d.userID, templateID, content, plan, thoughts, nil, nil)
	} else {
		resp, err = d.client.CreateDailyReport(ctx, d.userID, templateID, content, plan, thoughts, nil, nil)
	}

	if err != nil {
		return "", fmt.Errorf("create report failed: %w", err)
	}

	if resp.ErrCode != 0 {
		return "", fmt.Errorf("create report failed: %s", resp.ErrMsg)
	}

	reportName := "日报"
	if reportType == "weekly" {
		reportName = "周报"
	}
	return fmt.Sprintf("✅ %s created successfully! ID: %s", reportName, resp.Result), nil
}

func (d *DingTalkTool) calendarCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	title, _ := args["title"].(string)
	if title == "" {
		return "", fmt.Errorf("title is required for calendar_create")
	}

	eventDate, _ := args["event_date"].(string)
	eventTime, _ := args["event_time"].(string)
	location, _ := args["location"].(string)
	description, _ := args["content"].(string)

	duration := 60
	if d, ok := args["duration_minutes"].(float64); ok {
		duration = int(d)
	}

	// Parse date and time
	startTime := parseDateTime(eventDate, eventTime)
	if startTime == 0 {
		// Default to tomorrow 9:00 AM
		tomorrow := time.Now().AddDate(0, 0, 1)
		startTime = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 9, 0, 0, 0, time.Local).Unix() * 1000
	}

	endTime := startTime + int64(duration*60*1000)

	event := &dingtalk.CalendarEvent{
		Summary:     title,
		Description: description,
		Start: dingtalk.CalendarTime{
			TimeStamp: startTime,
			TimeZone:  "Asia/Shanghai",
		},
		End: dingtalk.CalendarTime{
			TimeStamp: endTime,
			TimeZone:  "Asia/Shanghai",
		},
		Visibility: "default",
		Reminders: []dingtalk.CalendarReminder{
			{Method: "app", Minutes: 15},
		},
	}

	if location != "" {
		event.Location = &dingtalk.CalendarLocation{
			DisplayName:  location,
			LocationType: "default",
		}
	}

	resp, err := d.client.CreateCalendarEvent(ctx, d.userID, event)
	if err != nil {
		return "", fmt.Errorf("create calendar event failed: %w", err)
	}

	return fmt.Sprintf("📅 Calendar event created successfully! ID: %s", resp.EventID), nil
}

// parseDate converts date string to millisecond timestamp
func parseDate(dateStr string) int64 {
	dateStr = strings.ToLower(dateStr)

	var t time.Time
	now := time.Now()

	switch dateStr {
	case "today":
		t = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
	case "tomorrow":
		tomorrow := now.AddDate(0, 0, 1)
		t = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 23, 59, 59, 0, now.Location())
	default:
		parsed, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return 0
		}
		t = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 59, 0, now.Location())
	}

	return t.Unix() * 1000
}

// parseDateTime parses date and time strings
func parseDateTime(dateStr, timeStr string) int64 {
	if dateStr == "" {
		return 0
	}

	now := time.Now()
	var t time.Time
	if dateStr == "today" {
		t = time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	} else if dateStr == "tomorrow" {
		tomorrow := now.AddDate(0, 0, 1)
		t = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 9, 0, 0, 0, now.Location())
	} else {
		parsed, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return 0
		}
		t = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 9, 0, 0, 0, now.Location())
	}

	hour, minute := 9, 0 // Default 9:00 AM
	if timeStr != "" {
		parts := strings.Split(timeStr, ":")
		if len(parts) == 2 {
			h, _ := strconv.Atoi(parts[0])
			m, _ := strconv.Atoi(parts[1])
			if h >= 0 && h < 24 {
				hour = h
			}
			if m >= 0 && m < 60 {
				minute = m
			}
		}
	}

	t = time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, now.Location())
	return t.Unix() * 1000
}
