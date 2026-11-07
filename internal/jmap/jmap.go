// Package jmap is a minimal JMAP client: fetch the Session, then send
// method calls to the API endpoint.  It knows nothing about any data type;
// the sync engine in package store builds the calls.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Capabilities this client uses.
const (
	CoreCapability  = "urn:ietf:params:jmap:core"
	TasksCapability = "urn:ietf:params:jmap:tasks"
)

// Client talks to one JMAP server.
type Client struct {
	HTTP     *http.Client
	APIURL   string
	Accounts map[string]Account
}

// Account is an entry in the Session's accounts.
type Account struct {
	Name       string `json:"name"`
	IsReadOnly bool   `json:"isReadOnly"`
}

// Open fetches the Session resource at sessionURL.
func Open(ctx context.Context, sessionURL string) (*Client, error) {
	c := &Client{HTTP: http.DefaultClient}
	req, err := http.NewRequestWithContext(ctx, "GET", sessionURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("jmap: session %s: %s: %s", sessionURL, resp.Status, bytes.TrimSpace(body))
	}
	var session struct {
		APIURL   string             `json:"apiUrl"`
		Accounts map[string]Account `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("jmap: decoding session: %w", err)
	}
	base, err := url.Parse(sessionURL)
	if err != nil {
		return nil, err
	}
	api, err := base.Parse(session.APIURL)
	if err != nil {
		return nil, fmt.Errorf("jmap: bad apiUrl %q: %w", session.APIURL, err)
	}
	c.APIURL, c.Accounts = api.String(), session.Accounts
	return c, nil
}

// Call is one method call.  ID is its call id, which the response
// carries back.
type Call struct {
	Name string
	Args map[string]any
	ID   string
}

func (c Call) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{c.Name, c.Args, c.ID})
}

// Response is one method response.  A method-level error has Name
// "error"; MethodError extracts it.
type Response struct {
	Name string
	Args map[string]any
	ID   string
}

func (r *Response) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return fmt.Errorf("jmap: invocation with %d parts", len(parts))
	}
	if err := json.Unmarshal(parts[0], &r.Name); err != nil {
		return err
	}
	if err := json.Unmarshal(parts[1], &r.Args); err != nil {
		return err
	}
	return json.Unmarshal(parts[2], &r.ID)
}

// MethodError is a method-level error response.
type MethodError struct {
	Type        string
	Description string
}

func (e *MethodError) Error() string {
	if e.Description != "" {
		return e.Type + ": " + e.Description
	}
	return e.Type
}

// Err returns the method error this response carries, or nil.
func (r Response) Err() *MethodError {
	if r.Name != "error" {
		return nil
	}
	e := &MethodError{}
	e.Type, _ = r.Args["type"].(string)
	e.Description, _ = r.Args["description"].(string)
	return e
}

// Do sends the calls in one request and returns the responses, in order.
func (c *Client) Do(ctx context.Context, calls ...Call) ([]Response, error) {
	body, err := json.Marshal(map[string]any{
		"using":       []string{CoreCapability, TasksCapability},
		"methodCalls": calls,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.APIURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("jmap: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	var out struct {
		MethodResponses []Response `json:"methodResponses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("jmap: decoding response: %w", err)
	}
	return out.MethodResponses, nil
}

// One sends a single call and returns its response's arguments, turning
// a method-level error into a Go error.
func (c *Client) One(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	rs, err := c.Do(ctx, Call{Name: name, Args: args, ID: "0"})
	if err != nil {
		return nil, err
	}
	if len(rs) != 1 {
		return nil, fmt.Errorf("jmap: %s: %d responses", name, len(rs))
	}
	if e := rs[0].Err(); e != nil {
		return nil, e
	}
	return rs[0].Args, nil
}
