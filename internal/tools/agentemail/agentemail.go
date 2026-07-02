package agentemail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ActionSetupCreate    = "setup_create"
	ActionListMessages   = "list_messages"
	ActionSearchMessages = "search_messages"
	ActionReadMessage    = "read_message"
	ActionWaitForMessage = "wait_for_message"
	ActionSendMessage    = "send_message"

	ProviderAgentMail = "agentmail"

	agentMailAPIKeyEnv  = "AGENTMAIL_API_KEY"
	agentMailBaseURLEnv = "AGENTMAIL_BASE_URL"

	defaultAgentMailBaseURL = "https://api.agentmail.to"
	defaultDisplayName      = "OpenCTO Agent"
	defaultLimit            = 10
	defaultWaitSeconds      = 60
	defaultPollSeconds      = 5
	maxWaitSeconds          = 570
	maxPollSeconds          = 60
	maxObservationBodyChars = 6000

	waitDeadlineBuffer = 30 * time.Second
)

var (
	ErrActionRequired      = errors.New("action is required")
	ErrUnsupportedAction   = errors.New("unsupported AgentEmail action")
	ErrAPIKeyRequired      = errors.New("AGENTMAIL_API_KEY is required")
	ErrInboxIDRequired     = errors.New("inbox_id is required")
	ErrMessageIDRequired   = errors.New("message_id is required")
	ErrQueryRequired       = errors.New("query is required")
	ErrRecipientRequired   = errors.New("to recipient is required")
	ErrMessageBodyRequired = errors.New("text or html body is required")
)

type Request struct {
	ProjectID   string `json:"-"`
	WorkItemID  string `json:"-"`
	ToolCallID  string `json:"-"`
	ActorID     string `json:"-"`
	ActorName   string `json:"-"`
	ChannelID   string `json:"-"`
	ChannelType string `json:"-"`
	ThreadID    string `json:"-"`

	Action              string   `json:"action"`
	InboxID             string   `json:"inbox_id"`
	DisplayName         string   `json:"display_name"`
	Username            string   `json:"username"`
	Domain              string   `json:"domain"`
	Query               string   `json:"query"`
	MessageID           string   `json:"message_id"`
	From                string   `json:"from"`
	To                  []string `json:"to"`
	Cc                  []string `json:"cc"`
	Bcc                 []string `json:"bcc"`
	ReplyTo             []string `json:"reply_to"`
	Subject             string   `json:"subject"`
	Text                string   `json:"text"`
	HTML                string   `json:"html"`
	Limit               int      `json:"limit"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
}

type Result struct {
	Action           string           `json:"action"`
	Status           string           `json:"status"`
	Provider         string           `json:"provider,omitempty"`
	Email            string           `json:"email,omitempty"`
	InboxID          string           `json:"inbox_id,omitempty"`
	MessageID        string           `json:"message_id,omitempty"`
	ThreadID         string           `json:"thread_id,omitempty"`
	Messages         []MessageSummary `json:"messages,omitempty"`
	Message          *Message         `json:"message,omitempty"`
	MemorySuggestion string           `json:"memory_suggestion,omitempty"`
}

type MessageSummary struct {
	InboxID         string   `json:"inbox_id,omitempty"`
	ThreadID        string   `json:"thread_id,omitempty"`
	MessageID       string   `json:"message_id,omitempty"`
	From            string   `json:"from,omitempty"`
	To              []string `json:"to,omitempty"`
	Subject         string   `json:"subject,omitempty"`
	Preview         string   `json:"preview,omitempty"`
	Timestamp       string   `json:"timestamp,omitempty"`
	AttachmentCount int      `json:"attachment_count,omitempty"`
}

type Message struct {
	InboxID       string            `json:"inbox_id,omitempty"`
	ThreadID      string            `json:"thread_id,omitempty"`
	MessageID     string            `json:"message_id,omitempty"`
	Labels        []string          `json:"labels,omitempty"`
	Timestamp     string            `json:"timestamp,omitempty"`
	From          string            `json:"from,omitempty"`
	ReplyTo       []string          `json:"reply_to,omitempty"`
	To            []string          `json:"to,omitempty"`
	Cc            []string          `json:"cc,omitempty"`
	Bcc           []string          `json:"bcc,omitempty"`
	Subject       string            `json:"subject,omitempty"`
	Preview       string            `json:"preview,omitempty"`
	Text          string            `json:"text,omitempty"`
	HTML          string            `json:"html,omitempty"`
	ExtractedText string            `json:"extracted_text,omitempty"`
	ExtractedHTML string            `json:"extracted_html,omitempty"`
	Attachments   []Attachment      `json:"attachments,omitempty"`
	InReplyTo     string            `json:"in_reply_to,omitempty"`
	References    []string          `json:"references,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Size          int               `json:"size,omitempty"`
	UpdatedAt     string            `json:"updated_at,omitempty"`
	CreatedAt     string            `json:"created_at,omitempty"`
}

type Attachment struct {
	AttachmentID       string `json:"attachment_id,omitempty"`
	Filename           string `json:"filename,omitempty"`
	Size               int    `json:"size,omitempty"`
	ContentType        string `json:"content_type,omitempty"`
	ContentDisposition string `json:"content_disposition,omitempty"`
	ContentID          string `json:"content_id,omitempty"`
}

type Inbox struct {
	PodID       string         `json:"pod_id,omitempty"`
	InboxID     string         `json:"inbox_id,omitempty"`
	Email       string         `json:"email,omitempty"`
	DisplayName string         `json:"display_name,omitempty"`
	ClientID    string         `json:"client_id,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	CreatedAt   string         `json:"created_at,omitempty"`
}

type CreateInboxRequest struct {
	Username    string         `json:"username,omitempty"`
	Domain      string         `json:"domain,omitempty"`
	DisplayName string         `json:"display_name,omitempty"`
	ClientID    string         `json:"client_id,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ListInboxesRequest struct {
	Limit     int
	PageToken string
}

type ListInboxesResponse struct {
	Count         int     `json:"count"`
	Limit         int     `json:"limit,omitempty"`
	NextPageToken string  `json:"next_page_token,omitempty"`
	Inboxes       []Inbox `json:"inboxes"`
}

type ListMessagesRequest struct {
	InboxID string
	Limit   int
	From    string
	To      []string
	Subject string
}

type ListMessagesResponse struct {
	Count         int       `json:"count"`
	Limit         int       `json:"limit,omitempty"`
	NextPageToken string    `json:"next_page_token,omitempty"`
	Messages      []Message `json:"messages"`
}

type SearchMessagesRequest struct {
	InboxID string
	Query   string
	Limit   int
}

type SearchMessagesResponse struct {
	Count         int       `json:"count"`
	Limit         int       `json:"limit,omitempty"`
	NextPageToken string    `json:"next_page_token,omitempty"`
	Messages      []Message `json:"messages"`
}

type SendMessageRequest struct {
	InboxID string
	ReplyTo []string `json:"reply_to,omitempty"`
	To      []string `json:"to,omitempty"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
}

type SendMessageResponse struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
}

type Client interface {
	ListInboxes(context.Context, ListInboxesRequest) (ListInboxesResponse, error)
	CreateInbox(context.Context, CreateInboxRequest) (Inbox, error)
	ListMessages(context.Context, ListMessagesRequest) (ListMessagesResponse, error)
	SearchMessages(context.Context, SearchMessagesRequest) (SearchMessagesResponse, error)
	ReadMessage(ctx context.Context, inboxID, messageID string) (Message, error)
	SendMessage(context.Context, SendMessageRequest) (SendMessageResponse, error)
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type SafeExecutor struct {
	Client Client
}

func NewSafeExecutor() *SafeExecutor {
	return &SafeExecutor{}
}

func (e *SafeExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	action := normalizeAction(req.Action)
	if action == "" {
		return Result{}, ErrActionRequired
	}
	result := Result{
		Action:   action,
		Status:   "succeeded",
		Provider: ProviderAgentMail,
	}

	switch action {
	case ActionSetupCreate:
		return e.setupCreate(ctx, req, result)
	case ActionListMessages:
		return e.listMessages(ctx, req, result)
	case ActionSearchMessages:
		return e.searchMessages(ctx, req, result)
	case ActionReadMessage:
		return e.readMessage(ctx, req, result)
	case ActionWaitForMessage:
		return e.waitForMessage(ctx, req, result)
	case ActionSendMessage:
		return e.sendMessage(ctx, req, result)
	default:
		return result, fmt.Errorf("%w: %s", ErrUnsupportedAction, action)
	}
}

func (e *SafeExecutor) setupCreate(ctx context.Context, req Request, result Result) (Result, error) {
	client, err := e.agentMailClient()
	if err != nil {
		return result, err
	}
	result.Provider = ProviderAgentMail
	clientID := stableClientID(req)
	if existing, ok, err := findInboxByClientID(ctx, client, clientID); err != nil {
		return result, err
	} else if ok {
		result.Email = strings.TrimSpace(existing.Email)
		result.InboxID = strings.TrimSpace(existing.InboxID)
		result.Status = "reused"
		result.MemorySuggestion = createdEmailMemory(result.Email, result.InboxID)
		return result, nil
	}

	inbox, err := client.CreateInbox(ctx, CreateInboxRequest{
		Username:    setupUsername(req, clientID),
		Domain:      strings.TrimSpace(req.Domain),
		DisplayName: firstNonEmpty(strings.TrimSpace(req.DisplayName), defaultDisplayName),
		ClientID:    clientID,
		Metadata:    setupMetadata(req),
	})
	if err != nil {
		if existing, ok, lookupErr := findInboxByClientID(ctx, client, clientID); lookupErr == nil && ok {
			result.Email = strings.TrimSpace(existing.Email)
			result.InboxID = strings.TrimSpace(existing.InboxID)
			result.Status = "reused"
			result.MemorySuggestion = createdEmailMemory(result.Email, result.InboxID)
			return result, nil
		}
		return result, err
	}
	result.Email = strings.TrimSpace(inbox.Email)
	result.InboxID = strings.TrimSpace(inbox.InboxID)
	result.MemorySuggestion = createdEmailMemory(result.Email, result.InboxID)
	return result, nil
}

func (e *SafeExecutor) listMessages(ctx context.Context, req Request, result Result) (Result, error) {
	client, err := e.agentMailClient()
	if err != nil {
		return result, err
	}
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return result, ErrInboxIDRequired
	}
	response, err := client.ListMessages(ctx, ListMessagesRequest{
		InboxID: inboxID,
		Limit:   normalizedLimit(req.Limit),
		From:    strings.TrimSpace(req.From),
		To:      trimStringList(req.To),
		Subject: strings.TrimSpace(req.Subject),
	})
	if err != nil {
		return result, err
	}
	result.Provider = ProviderAgentMail
	result.InboxID = inboxID
	result.Messages = summarizeMessages(response.Messages)
	return result, nil
}

func (e *SafeExecutor) searchMessages(ctx context.Context, req Request, result Result) (Result, error) {
	client, err := e.agentMailClient()
	if err != nil {
		return result, err
	}
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return result, ErrInboxIDRequired
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return result, ErrQueryRequired
	}
	response, err := client.SearchMessages(ctx, SearchMessagesRequest{
		InboxID: inboxID,
		Query:   query,
		Limit:   normalizedLimit(req.Limit),
	})
	if err != nil {
		return result, err
	}
	result.Provider = ProviderAgentMail
	result.InboxID = inboxID
	result.Messages = summarizeMessages(response.Messages)
	return result, nil
}

func (e *SafeExecutor) readMessage(ctx context.Context, req Request, result Result) (Result, error) {
	client, err := e.agentMailClient()
	if err != nil {
		return result, err
	}
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return result, ErrInboxIDRequired
	}
	messageID := strings.TrimSpace(req.MessageID)
	if messageID == "" {
		return result, ErrMessageIDRequired
	}
	message, err := client.ReadMessage(ctx, inboxID, messageID)
	if err != nil {
		return result, err
	}
	result.Provider = ProviderAgentMail
	result.InboxID = inboxID
	result.MessageID = message.MessageID
	result.ThreadID = message.ThreadID
	result.Message = &message
	return result, nil
}

func (e *SafeExecutor) waitForMessage(ctx context.Context, req Request, result Result) (Result, error) {
	client, err := e.agentMailClient()
	if err != nil {
		return result, err
	}
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return result, ErrInboxIDRequired
	}
	timeout := normalizedWait(req.TimeoutSeconds)
	poll := normalizedPoll(req.PollIntervalSeconds)
	deadline := waitDeadline(ctx, timeout)
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result.Provider = ProviderAgentMail
	result.InboxID = inboxID

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !time.Now().Before(deadline) {
			result.Status = "not_found"
			return result, nil
		}
		messages, err := findMessages(waitCtx, client, req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && !time.Now().Before(deadline) {
				result.Status = "not_found"
				return result, nil
			}
			return result, err
		}
		if len(messages) > 0 {
			messageID := strings.TrimSpace(messages[0].MessageID)
			if messageID == "" {
				result.Messages = summarizeMessages(messages)
				return result, nil
			}
			message, err := client.ReadMessage(waitCtx, inboxID, messageID)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && !time.Now().Before(deadline) {
					result.Status = "not_found"
					return result, nil
				}
				return result, err
			}
			result.MessageID = message.MessageID
			result.ThreadID = message.ThreadID
			result.Message = &message
			return result, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			result.Status = "not_found"
			return result, nil
		}
		sleep := time.Duration(poll) * time.Second
		if remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-waitCtx.Done():
			timer.Stop()
			if err := ctx.Err(); err != nil {
				return result, err
			}
			result.Status = "not_found"
			return result, nil
		case <-timer.C:
		}
	}
}

func (e *SafeExecutor) sendMessage(ctx context.Context, req Request, result Result) (Result, error) {
	client, err := e.agentMailClient()
	if err != nil {
		return result, err
	}
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return result, ErrInboxIDRequired
	}
	to := trimStringList(req.To)
	if len(to) == 0 {
		return result, ErrRecipientRequired
	}
	text := strings.TrimSpace(req.Text)
	html := strings.TrimSpace(req.HTML)
	if text == "" && html == "" {
		return result, ErrMessageBodyRequired
	}
	response, err := client.SendMessage(ctx, SendMessageRequest{
		InboxID: inboxID,
		ReplyTo: trimStringList(req.ReplyTo),
		To:      to,
		Cc:      trimStringList(req.Cc),
		Bcc:     trimStringList(req.Bcc),
		Subject: strings.TrimSpace(req.Subject),
		Text:    text,
		HTML:    html,
	})
	if err != nil {
		return result, err
	}
	result.Provider = ProviderAgentMail
	result.InboxID = inboxID
	result.MessageID = strings.TrimSpace(response.MessageID)
	result.ThreadID = strings.TrimSpace(response.ThreadID)
	return result, nil
}

func (e *SafeExecutor) agentMailClient() (Client, error) {
	if e.Client != nil {
		return e.Client, nil
	}
	return NewAgentMailHTTPClientFromEnv()
}

type AgentMailHTTPClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewAgentMailHTTPClientFromEnv() (*AgentMailHTTPClient, error) {
	apiKey := strings.TrimSpace(os.Getenv(agentMailAPIKeyEnv))
	if apiKey == "" {
		return nil, ErrAPIKeyRequired
	}
	baseURL := firstNonEmpty(strings.TrimSpace(os.Getenv(agentMailBaseURLEnv)), defaultAgentMailBaseURL)
	return NewAgentMailHTTPClient(baseURL, apiKey, nil)
}

func NewAgentMailHTTPClient(baseURL, apiKey string, httpClient *http.Client) (*AgentMailHTTPClient, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, ErrAPIKeyRequired
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultAgentMailBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse AgentMail base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("AgentMail base URL must include scheme and host")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &AgentMailHTTPClient{BaseURL: parsed.String(), APIKey: apiKey, HTTPClient: httpClient}, nil
}

func (c *AgentMailHTTPClient) ListInboxes(ctx context.Context, req ListInboxesRequest) (ListInboxesResponse, error) {
	endpoint, err := c.endpoint("inboxes")
	if err != nil {
		return ListInboxesResponse{}, err
	}
	query := endpoint.Query()
	if req.Limit > 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	if token := strings.TrimSpace(req.PageToken); token != "" {
		query.Set("page_token", token)
	}
	endpoint.RawQuery = query.Encode()
	var response ListInboxesResponse
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return ListInboxesResponse{}, err
	}
	return response, nil
}

func (c *AgentMailHTTPClient) CreateInbox(ctx context.Context, req CreateInboxRequest) (Inbox, error) {
	endpoint, err := c.endpoint("inboxes")
	if err != nil {
		return Inbox{}, err
	}
	var response Inbox
	if err := c.doJSON(ctx, http.MethodPost, endpoint, req, &response); err != nil {
		return Inbox{}, err
	}
	return response, nil
}

func (c *AgentMailHTTPClient) ListMessages(ctx context.Context, req ListMessagesRequest) (ListMessagesResponse, error) {
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return ListMessagesResponse{}, ErrInboxIDRequired
	}
	endpoint, err := c.endpoint("inboxes", inboxID, "messages")
	if err != nil {
		return ListMessagesResponse{}, err
	}
	query := endpoint.Query()
	if req.Limit > 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	addJSONQuery(query, "from", singleStringList(req.From))
	addJSONQuery(query, "to", trimStringList(req.To))
	addJSONQuery(query, "subject", singleStringList(req.Subject))
	endpoint.RawQuery = query.Encode()

	var response ListMessagesResponse
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return ListMessagesResponse{}, err
	}
	return response, nil
}

func (c *AgentMailHTTPClient) SearchMessages(ctx context.Context, req SearchMessagesRequest) (SearchMessagesResponse, error) {
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return SearchMessagesResponse{}, ErrInboxIDRequired
	}
	queryText := strings.TrimSpace(req.Query)
	if queryText == "" {
		return SearchMessagesResponse{}, ErrQueryRequired
	}
	endpoint, err := c.endpoint("inboxes", inboxID, "messages", "search")
	if err != nil {
		return SearchMessagesResponse{}, err
	}
	query := endpoint.Query()
	query.Set("q", queryText)
	if req.Limit > 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	endpoint.RawQuery = query.Encode()

	var response SearchMessagesResponse
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return SearchMessagesResponse{}, err
	}
	return response, nil
}

func (c *AgentMailHTTPClient) ReadMessage(ctx context.Context, inboxID, messageID string) (Message, error) {
	inboxID = strings.TrimSpace(inboxID)
	if inboxID == "" {
		return Message{}, ErrInboxIDRequired
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return Message{}, ErrMessageIDRequired
	}
	endpoint, err := c.endpoint("inboxes", inboxID, "messages", messageID)
	if err != nil {
		return Message{}, err
	}
	var response Message
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return Message{}, err
	}
	return response, nil
}

func (c *AgentMailHTTPClient) SendMessage(ctx context.Context, req SendMessageRequest) (SendMessageResponse, error) {
	inboxID := strings.TrimSpace(req.InboxID)
	if inboxID == "" {
		return SendMessageResponse{}, ErrInboxIDRequired
	}
	endpoint, err := c.endpoint("inboxes", inboxID, "messages", "send")
	if err != nil {
		return SendMessageResponse{}, err
	}
	body := struct {
		ReplyTo []string `json:"reply_to,omitempty"`
		To      []string `json:"to,omitempty"`
		Cc      []string `json:"cc,omitempty"`
		Bcc     []string `json:"bcc,omitempty"`
		Subject string   `json:"subject,omitempty"`
		Text    string   `json:"text,omitempty"`
		HTML    string   `json:"html,omitempty"`
	}{
		ReplyTo: trimStringList(req.ReplyTo),
		To:      trimStringList(req.To),
		Cc:      trimStringList(req.Cc),
		Bcc:     trimStringList(req.Bcc),
		Subject: strings.TrimSpace(req.Subject),
		Text:    strings.TrimSpace(req.Text),
		HTML:    strings.TrimSpace(req.HTML),
	}
	var response SendMessageResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint, body, &response); err != nil {
		return SendMessageResponse{}, err
	}
	return response, nil
}

func (c *AgentMailHTTPClient) endpoint(parts ...string) (*url.URL, error) {
	allParts := append([]string{"v0"}, parts...)
	joined, err := url.JoinPath(c.BaseURL, allParts...)
	if err != nil {
		return nil, err
	}
	return url.Parse(joined)
}

func (c *AgentMailHTTPClient) doJSON(ctx context.Context, method string, endpoint *url.URL, body any, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode AgentMail request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return apiError(response)
	}
	if target == nil {
		io.Copy(io.Discard, response.Body)
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode AgentMail response: %w", err)
	}
	return nil
}

type APIError struct {
	StatusCode int
	Body       string
}

func (e APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("AgentMail API request failed with status %d", e.StatusCode)
	}
	return fmt.Sprintf("AgentMail API request failed with status %d: %s", e.StatusCode, body)
}

func apiError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return APIError{
		StatusCode: response.StatusCode,
		Body:       string(body),
	}
}

func FormatObservation(result Result, err error) string {
	var builder strings.Builder
	action := firstNonEmpty(result.Action, "unknown")
	if err != nil {
		builder.WriteString("AgentEmail ")
		builder.WriteString(action)
		builder.WriteString(" failed.\n")
		builder.WriteString("error: ")
		builder.WriteString(err.Error())
		return builder.String()
	}
	builder.WriteString("AgentEmail ")
	builder.WriteString(action)
	builder.WriteString(" ")
	builder.WriteString(firstNonEmpty(result.Status, "succeeded"))
	builder.WriteString(".")
	writeKV(&builder, "provider", result.Provider)
	writeKV(&builder, "email", result.Email)
	writeKV(&builder, "inbox_id", result.InboxID)
	writeKV(&builder, "message_id", result.MessageID)
	writeKV(&builder, "thread_id", result.ThreadID)
	if result.MemorySuggestion != "" {
		writeKV(&builder, "memory_suggestion", result.MemorySuggestion)
		builder.WriteString("\nmemory_tool: propose this with MemoryProposeAdd or MemoryProposeUpdate; recommended add fields are scope=user, kind=identity, tags=")
		builder.WriteString(strings.Join(recommendedMemoryTags(), ","))
		builder.WriteString(", pinned=true, confidence=1.0.")
	}
	if len(result.Messages) > 0 {
		builder.WriteString("\nmessages:")
		for _, message := range result.Messages {
			builder.WriteString("\n- ")
			builder.WriteString(firstNonEmpty(message.MessageID, "(no message id)"))
			writeInlineKV(&builder, "from", message.From)
			writeInlineKV(&builder, "subject", message.Subject)
			writeInlineKV(&builder, "timestamp", message.Timestamp)
			writeInlineKV(&builder, "preview", truncate(message.Preview, 240))
		}
	} else if result.Action == ActionListMessages || result.Action == ActionSearchMessages {
		builder.WriteString("\nmessages: none")
	}
	if result.Message != nil {
		writeMessageObservation(&builder, *result.Message)
	}
	return builder.String()
}

func writeMessageObservation(builder *strings.Builder, message Message) {
	builder.WriteString("\nmessage:")
	writeKV(builder, "from", message.From)
	if len(message.To) > 0 {
		writeKV(builder, "to", strings.Join(message.To, ", "))
	}
	writeKV(builder, "subject", message.Subject)
	writeKV(builder, "timestamp", message.Timestamp)
	body := firstNonEmpty(strings.TrimSpace(message.ExtractedText), strings.TrimSpace(message.Text))
	if body != "" {
		builder.WriteString("\ntext:\n")
		builder.WriteString(truncate(body, maxObservationBodyChars))
		return
	}
	html := firstNonEmpty(strings.TrimSpace(message.ExtractedHTML), strings.TrimSpace(message.HTML))
	if html != "" {
		builder.WriteString("\nhtml:\n")
		builder.WriteString(truncate(html, maxObservationBodyChars))
	}
}

func writeKV(builder *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(key)
	builder.WriteString(": ")
	builder.WriteString(value)
}

func writeInlineKV(builder *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	builder.WriteString(" ")
	builder.WriteString(key)
	builder.WriteString("=")
	builder.WriteString(strconv.Quote(value))
}

func findInboxByClientID(ctx context.Context, client Client, clientID string) (Inbox, bool, error) {
	pageToken := ""
	for {
		response, err := client.ListInboxes(ctx, ListInboxesRequest{Limit: 100, PageToken: pageToken})
		if err != nil {
			return Inbox{}, false, err
		}
		for _, inbox := range response.Inboxes {
			if strings.TrimSpace(inbox.ClientID) == clientID {
				return inbox, true, nil
			}
		}
		next := strings.TrimSpace(response.NextPageToken)
		if next == "" || next == pageToken {
			return Inbox{}, false, nil
		}
		pageToken = next
	}
}

func findMessages(ctx context.Context, client Client, req Request) ([]Message, error) {
	inboxID := strings.TrimSpace(req.InboxID)
	if query := strings.TrimSpace(req.Query); query != "" {
		limit := 1
		if hasMessageFilters(req) {
			limit = 100
		}
		response, err := client.SearchMessages(ctx, SearchMessagesRequest{
			InboxID: inboxID,
			Query:   query,
			Limit:   limit,
		})
		if err != nil {
			return nil, err
		}
		return filterMessages(response.Messages, req), nil
	}
	response, err := client.ListMessages(ctx, ListMessagesRequest{
		InboxID: inboxID,
		Limit:   1,
		From:    strings.TrimSpace(req.From),
		To:      trimStringList(req.To),
		Subject: strings.TrimSpace(req.Subject),
	})
	return response.Messages, err
}

func hasMessageFilters(req Request) bool {
	return strings.TrimSpace(req.From) != "" ||
		len(trimStringList(req.To)) > 0 ||
		strings.TrimSpace(req.Subject) != ""
}

func filterMessages(messages []Message, req Request) []Message {
	if !hasMessageFilters(req) {
		return messages
	}
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		if messageMatchesFilters(message, req) {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func messageMatchesFilters(message Message, req Request) bool {
	if from := strings.TrimSpace(req.From); from != "" && !containsFold(message.From, from) {
		return false
	}
	if subject := strings.TrimSpace(req.Subject); subject != "" && !containsFold(message.Subject, subject) {
		return false
	}
	for _, filter := range trimStringList(req.To) {
		if !stringListContainsFold(message.To, filter) {
			return false
		}
	}
	return true
}

func stringListContainsFold(values []string, needle string) bool {
	for _, value := range values {
		if containsFold(value, needle) {
			return true
		}
	}
	return false
}

func containsFold(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}

func stableClientID(req Request) string {
	identity := firstNonEmpty(strings.TrimSpace(req.ActorID), strings.TrimSpace(req.ChannelID), strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.WorkItemID), "default")
	sum := sha256.Sum256([]byte("opencto-agent-email:" + identity))
	return "opencto-agent-email-" + hex.EncodeToString(sum[:])[:16]
}

func setupUsername(req Request, clientID string) string {
	username := sanitizeUsername(req.Username)
	if username != "" {
		return username
	}
	return "opencto-" + strings.TrimPrefix(clientID, "opencto-agent-email-")[:10]
}

func setupMetadata(req Request) map[string]any {
	metadata := map[string]any{
		"created_by": "opencto",
		"purpose":    "agent-email",
	}
	for key, value := range map[string]string{
		"project_id":   req.ProjectID,
		"work_item_id": req.WorkItemID,
		"tool_call_id": req.ToolCallID,
		"actor_id":     req.ActorID,
		"actor_name":   req.ActorName,
		"channel_id":   req.ChannelID,
		"channel_type": req.ChannelType,
		"thread_id":    req.ThreadID,
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			metadata[key] = trimmed
		}
	}
	return metadata
}

var usernameInvalidChars = regexp.MustCompile(`[^a-z0-9._-]+`)

func sanitizeUsername(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = usernameInvalidChars.ReplaceAllString(value, "-")
	value = strings.Trim(value, ".-_")
	if len(value) > 48 {
		value = strings.Trim(value[:48], ".-_")
	}
	return value
}

func normalizeAction(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizedLimit(value int) int {
	switch {
	case value <= 0:
		return defaultLimit
	case value > 100:
		return 100
	default:
		return value
	}
}

func normalizedWait(value int) int {
	switch {
	case value <= 0:
		return defaultWaitSeconds
	case value > maxWaitSeconds:
		return maxWaitSeconds
	default:
		return value
	}
}

func NormalizeWaitSeconds(value int) int {
	return normalizedWait(value)
}

func waitDeadline(ctx context.Context, timeoutSeconds int) time.Time {
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		buffered := ctxDeadline.Add(-waitDeadlineBuffer)
		if buffered.Before(deadline) {
			return buffered
		}
	}
	return deadline
}

func normalizedPoll(value int) int {
	switch {
	case value <= 0:
		return defaultPollSeconds
	case value > maxPollSeconds:
		return maxPollSeconds
	default:
		return value
	}
}

func addJSONQuery(values url.Values, key string, value any) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return
		}
	case []string:
		if len(typed) == 0 {
			return
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	values.Set(key, string(encoded))
}

func singleStringList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return []string{value}
}

func recommendedMemoryTags() []string {
	return []string{"identity", "agent-email", "agentmail"}
}

func summarizeMessages(messages []Message) []MessageSummary {
	if len(messages) == 0 {
		return nil
	}
	summaries := make([]MessageSummary, 0, len(messages))
	for _, message := range messages {
		summaries = append(summaries, MessageSummary{
			InboxID:         message.InboxID,
			ThreadID:        message.ThreadID,
			MessageID:       message.MessageID,
			From:            message.From,
			To:              append([]string(nil), message.To...),
			Subject:         message.Subject,
			Preview:         message.Preview,
			Timestamp:       message.Timestamp,
			AttachmentCount: len(message.Attachments),
		})
	}
	return summaries
}

func createdEmailMemory(email, inboxID string) string {
	email = strings.TrimSpace(email)
	inboxID = strings.TrimSpace(inboxID)
	if inboxID == "" {
		return fmt.Sprintf("OpenCTO AgentEmail for third-party service accounts is %s.", email)
	}
	return fmt.Sprintf("OpenCTO AgentEmail for third-party service accounts is %s; AgentMail inbox id is %s.", email, inboxID)
}

func trimStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}

func truncate(value string, maxChars int) string {
	if maxChars <= 0 || len(value) <= maxChars {
		return value
	}
	if maxChars <= 32 {
		return value[:maxChars]
	}
	return value[:maxChars] + "\n[truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
