package agentemail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSetupCreateReusesExistingAgentMailInboxByStableClientID(t *testing.T) {
	t.Parallel()

	req := Request{
		ProjectID: "project-1",
		ActorID:   "actor-1",
		Action:    ActionSetupCreate,
	}
	clientID := stableClientID(req)
	client := &fakeClient{
		listInboxesResponse: ListInboxesResponse{
			Inboxes: []Inbox{{
				InboxID:  "inb_existing",
				Email:    "opencto@example.agentmail.to",
				ClientID: clientID,
			}},
		},
	}
	result, err := (&SafeExecutor{Client: client}).Run(context.Background(), req)
	if err != nil {
		t.Fatalf("setup create: %v", err)
	}
	if result.Status != "reused" || result.InboxID != "inb_existing" || result.Email != "opencto@example.agentmail.to" {
		t.Fatalf("unexpected reused result: %#v", result)
	}
	if client.createCalled {
		t.Fatalf("setup_create should not create a duplicate inbox when client_id already exists")
	}
	if !strings.Contains(result.MemorySuggestion, "AgentMail inbox id is inb_existing") {
		t.Fatalf("unexpected memory suggestion: %q", result.MemorySuggestion)
	}
}

func TestAgentMailHTTPClientSendMessageUsesBearerAuthAndPath(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenAuth string
	var seenBody struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message_id":"msg_1","thread_id":"thr_1"}`))
	}))
	defer server.Close()

	client, err := NewAgentMailHTTPClient(server.URL, "secret-token", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.SendMessage(context.Background(), SendMessageRequest{
		InboxID: "inb_1",
		To:      []string{"user@example.com"},
		Subject: "Verify",
		Text:    "Hello",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	if seenPath != "/v0/inboxes/inb_1/messages/send" || seenAuth != "Bearer secret-token" {
		t.Fatalf("unexpected request path/auth: path=%q auth=%q", seenPath, seenAuth)
	}
	if len(seenBody.To) != 1 || seenBody.To[0] != "user@example.com" || seenBody.Subject != "Verify" || seenBody.Text != "Hello" {
		t.Fatalf("unexpected request body: %#v", seenBody)
	}
	if result.MessageID != "msg_1" || result.ThreadID != "thr_1" {
		t.Fatalf("unexpected send result: %#v", result)
	}
}

func TestAgentMailHTTPClientListMessagesEncodesFiltersAsArrays(t *testing.T) {
	t.Parallel()

	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"count":0,"messages":[]}`))
	}))
	defer server.Close()

	client, err := NewAgentMailHTTPClient(server.URL, "secret-token", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.ListMessages(context.Background(), ListMessagesRequest{
		InboxID: "inb_1",
		From:    "cloudflare",
		To:      []string{"ops@example.com"},
		Subject: "verify",
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if seenQuery.Get("from") != `["cloudflare"]` || seenQuery.Get("to") != `["ops@example.com"]` || seenQuery.Get("subject") != `["verify"]` {
		t.Fatalf("expected array-encoded filters, got %#v", seenQuery)
	}
}

func TestWaitForMessageDoesNotPollAfterDeadline(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	result, err := (&SafeExecutor{Client: client}).Run(context.Background(), Request{
		Action:              ActionWaitForMessage,
		InboxID:             "inb_1",
		TimeoutSeconds:      1,
		PollIntervalSeconds: 1,
	})
	if err != nil {
		t.Fatalf("wait for message: %v", err)
	}
	if result.Status != "not_found" {
		t.Fatalf("expected not_found result, got %#v", result)
	}
	if len(client.listMessagesRequests) != 1 {
		t.Fatalf("expected one poll before deadline, got %d", len(client.listMessagesRequests))
	}
}

func TestWaitForMessageReturnsBeforeParentContextDeadline(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(50*time.Millisecond))
	defer cancel()

	result, err := (&SafeExecutor{Client: client}).Run(ctx, Request{
		Action:              ActionWaitForMessage,
		InboxID:             "inb_1",
		TimeoutSeconds:      600,
		PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatalf("wait for message: %v", err)
	}
	if result.Status != "not_found" {
		t.Fatalf("expected not_found result, got %#v", result)
	}
	if len(client.listMessagesRequests) != 0 {
		t.Fatalf("expected deadline buffer to avoid polling when context is nearly expired, got %d polls", len(client.listMessagesRequests))
	}
}

func TestWaitForMessageAppliesFiltersAfterSearch(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		searchMessagesResponse: SearchMessagesResponse{
			Messages: []Message{
				{
					MessageID: "msg_unrelated",
					From:      "noreply@example.com",
					To:        []string{"agent@example.com"},
					Subject:   "verification code",
				},
				{
					MessageID: "msg_cloudflare",
					From:      "Cloudflare <noreply@cloudflare.com>",
					To:        []string{"agent@example.com"},
					Subject:   "Your verification code",
				},
			},
		},
		readMessageResponse: Message{
			MessageID: "msg_cloudflare",
			ThreadID:  "thr_1",
			From:      "Cloudflare <noreply@cloudflare.com>",
			To:        []string{"agent@example.com"},
			Subject:   "Your verification code",
			Text:      "123456",
		},
	}
	result, err := (&SafeExecutor{Client: client}).Run(context.Background(), Request{
		Action:              ActionWaitForMessage,
		InboxID:             "inb_1",
		Query:               "verification",
		From:                "cloudflare.com",
		To:                  []string{"agent@example.com"},
		Subject:             "verification code",
		TimeoutSeconds:      1,
		PollIntervalSeconds: 1,
	})
	if err != nil {
		t.Fatalf("wait for message: %v", err)
	}
	if result.MessageID != "msg_cloudflare" || client.readMessageID != "msg_cloudflare" {
		t.Fatalf("expected matching Cloudflare message to be read, result=%#v read=%q", result, client.readMessageID)
	}
	if len(client.searchMessagesRequests) != 1 || client.searchMessagesRequests[0].Limit != 100 {
		t.Fatalf("expected filtered wait to search a wider page, got %#v", client.searchMessagesRequests)
	}
}

func TestNormalizeWaitSecondsCapsBelowActivityTimeout(t *testing.T) {
	t.Parallel()

	if got := NormalizeWaitSeconds(600); got != maxWaitSeconds || got >= 600 {
		t.Fatalf("expected max wait below 600s, got %d", got)
	}
}

func TestFormatObservationRecommendsAgentEmailMemoryTags(t *testing.T) {
	t.Parallel()

	agentmail := FormatObservation(Result{
		Action:           ActionSetupCreate,
		Status:           "succeeded",
		Provider:         ProviderAgentMail,
		Email:            "ops@example.agentmail.to",
		InboxID:          "inb_1",
		MemorySuggestion: "OpenCTO AgentEmail for third-party service accounts is ops@example.agentmail.to; AgentMail inbox id is inb_1.",
	}, nil)
	if !strings.Contains(agentmail, "tags=identity,agent-email,agentmail") || strings.Contains(agentmail, "tags=onboarding") {
		t.Fatalf("AgentMail setup should recommend agentmail tag:\n%s", agentmail)
	}
}

type fakeClient struct {
	listInboxesResponse    ListInboxesResponse
	createInboxResponse    Inbox
	listMessagesResponse   ListMessagesResponse
	searchMessagesResponse SearchMessagesResponse
	readMessageResponse    Message
	sendMessageResponse    SendMessageResponse
	listMessagesRequests   []ListMessagesRequest
	searchMessagesRequests []SearchMessagesRequest
	readMessageID          string
	createCalled           bool
}

func (f *fakeClient) ListInboxes(context.Context, ListInboxesRequest) (ListInboxesResponse, error) {
	return f.listInboxesResponse, nil
}

func (f *fakeClient) CreateInbox(context.Context, CreateInboxRequest) (Inbox, error) {
	f.createCalled = true
	return f.createInboxResponse, nil
}

func (f *fakeClient) ListMessages(_ context.Context, req ListMessagesRequest) (ListMessagesResponse, error) {
	f.listMessagesRequests = append(f.listMessagesRequests, req)
	return f.listMessagesResponse, nil
}

func (f *fakeClient) SearchMessages(_ context.Context, req SearchMessagesRequest) (SearchMessagesResponse, error) {
	f.searchMessagesRequests = append(f.searchMessagesRequests, req)
	return f.searchMessagesResponse, nil
}

func (f *fakeClient) ReadMessage(_ context.Context, _, messageID string) (Message, error) {
	f.readMessageID = messageID
	return f.readMessageResponse, nil
}

func (f *fakeClient) SendMessage(context.Context, SendMessageRequest) (SendMessageResponse, error) {
	return f.sendMessageResponse, nil
}
