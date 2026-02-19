package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
)

// TodoTask represents a DingTalk todo task
type TodoTask struct {
	TaskID      string `json:"taskId,omitempty"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	DueTime     int64  `json:"dueTime,omitempty"`
	IsDone      bool   `json:"isDone,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

// CreateTodoResponse represents the response from creating a todo
type CreateTodoResponse struct {
	ID string `json:"id"`
}

// ListTodosResponse represents the response from listing todos
type ListTodosResponse struct {
	TodoCards []TodoTask `json:"todoCards"`
}

// CreateTodo creates a new todo task for the specified user
func (c *Client) CreateTodo(ctx context.Context, unionID string, task *TodoTask) (*CreateTodoResponse, error) {
	url := fmt.Sprintf("%s/todo/users/%s/tasks", BaseURL, unionID)

	body := map[string]interface{}{
		"subject":            task.Subject,
		"isOnlyShowExecutor": false,
	}

	if task.Description != "" {
		body["description"] = task.Description
	}
	if task.DueTime > 0 {
		body["dueTime"] = task.DueTime
	}

	respBody, err := c.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("create todo failed: %w", err)
	}

	var result CreateTodoResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// ListTodos lists todo tasks for the specified user
func (c *Client) ListTodos(ctx context.Context, unionID string, isDone *bool, pageSize int) (*ListTodosResponse, error) {
	url := fmt.Sprintf("%s/todo/users/%s/org/tasks/query", BaseURL, unionID)

	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}

	body := map[string]interface{}{
		"pageSize": pageSize,
	}

	if isDone != nil {
		body["isDone"] = *isDone
	}

	respBody, err := c.doRequest(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("list todos failed: %w", err)
	}

	var result ListTodosResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// CompleteTodo marks a todo task as completed
func (c *Client) CompleteTodo(ctx context.Context, unionID, taskID string) error {
	url := fmt.Sprintf("%s/todo/users/%s/tasks/%s/finish", BaseURL, unionID, taskID)

	_, err := c.doRequest(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("complete todo failed: %w", err)
	}

	return nil
}

// UpdateTodo updates a todo task
func (c *Client) UpdateTodo(ctx context.Context, unionID, taskID string, updates map[string]interface{}) error {
	url := fmt.Sprintf("%s/todo/users/%s/tasks/%s", BaseURL, unionID, taskID)

	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}

	_, err := c.doRequest(ctx, "PUT", url, updates)
	if err != nil {
		return fmt.Errorf("update todo failed: %w", err)
	}

	return nil
}

// DeleteTodo deletes a todo task
func (c *Client) DeleteTodo(ctx context.Context, unionID, taskID string) error {
	url := fmt.Sprintf("%s/todo/users/%s/tasks/%s", BaseURL, unionID, taskID)

	_, err := c.doRequest(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("delete todo failed: %w", err)
	}

	return nil
}
