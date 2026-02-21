package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
)

// CalendarEvent represents a DingTalk calendar event
type CalendarEvent struct {
	EventID     string             `json:"eventId,omitempty"`
	Summary     string             `json:"summary"`
	Description string             `json:"description,omitempty"`
	Start       CalendarTime       `json:"start"`
	End         CalendarTime       `json:"end"`
	Location    *CalendarLocation  `json:"location,omitempty"`
	Reminders   []CalendarReminder `json:"reminders,omitempty"`
	Visibility  string             `json:"visibility,omitempty"`
}

// CalendarTime represents event time
type CalendarTime struct {
	TimeStamp int64  `json:"timeStamp"`
	TimeZone  string `json:"timeZone"`
}

// CalendarLocation represents event location
type CalendarLocation struct {
	DisplayName  string `json:"displayName"`
	LocationType string `json:"locationType"`
}

// CalendarReminder represents a reminder setting
type CalendarReminder struct {
	Method  string `json:"method"`  // "app", "sms"
	Minutes int    `json:"minutes"` // minutes before event
}

// CreateEventResponse represents the response from creating a calendar event
type CreateEventResponse struct {
	EventID string `json:"eventId"`
}

// CreateCalendarEvent creates a new calendar event
func (c *Client) CreateCalendarEvent(ctx context.Context, userID string, event *CalendarEvent) (*CreateEventResponse, error) {
	url := fmt.Sprintf("%s/calendar/events?userId=%s", BaseURL, userID)

	eventData := map[string]interface{}{
		"summary":    event.Summary,
		"start":      event.Start,
		"end":        event.End,
		"visibility": event.Visibility,
	}

	if event.Description != "" {
		eventData["description"] = event.Description
	}
	if event.Location != nil {
		eventData["location"] = event.Location
	}
	if len(event.Reminders) > 0 {
		eventData["reminders"] = event.Reminders
	}

	respBody, err := c.doRequest(ctx, "POST", url, eventData)
	if err != nil {
		return nil, fmt.Errorf("create calendar event failed: %w", err)
	}

	var result CreateEventResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// ListCalendarEvents lists calendar events for a user
func (c *Client) ListCalendarEvents(ctx context.Context, userID string, startTime, endTime int64) ([]CalendarEvent, error) {
	url := fmt.Sprintf("%s/calendar/users/%s/events", BaseURL, userID)

	body := map[string]interface{}{
		"startTime": startTime,
		"endTime":   endTime,
		"timeZone":  "Asia/Shanghai",
	}

	respBody, err := c.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("list calendar events failed: %w", err)
	}

	var result struct {
		Events []CalendarEvent `json:"events"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Events, nil
}

// DeleteCalendarEvent deletes a calendar event
func (c *Client) DeleteCalendarEvent(ctx context.Context, userID, eventID string) error {
	url := fmt.Sprintf("%s/calendar/users/%s/events/%s", BaseURL, userID, eventID)

	_, err := c.doRequest(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("delete calendar event failed: %w", err)
	}

	return nil
}
