package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestPolicyValidateSessionCreate(t *testing.T) {
	policy := newPolicyEngine()
	if perr := policy.validateSessionCreate(protocol.SessionCreateParams{AgentID: "codex"}); perr != nil {
		t.Fatalf("default create rejected: %v", perr)
	}
	if perr := policy.validateSessionCreate(protocol.SessionCreateParams{AgentID: "codex", PermissionMode: "wild"}); perr == nil {
		t.Fatal("invalid permission mode should be rejected")
	}
	if perr := policy.validateSessionCreate(protocol.SessionCreateParams{PermissionMode: protocol.PermissionDefault}); perr == nil {
		t.Fatal("missing agent id should be rejected")
	}
	if perr := policy.validateSessionCreate(protocol.SessionCreateParams{
		AgentID: "codex", Cwd: string(filepath.Separator), PermissionMode: protocol.PermissionFull,
	}); perr == nil || !strings.Contains(perr.Message, protocol.PermissionFull) {
		t.Fatalf("full permission at root should be rejected, got %v", perr)
	}
}

func TestPolicyValidateTaskSend(t *testing.T) {
	policy := newPolicyEngine()
	valid := protocol.TaskSendParams{
		SessionID:   "s_1",
		Prompt:      "describe",
		Attachments: []protocol.Attachment{{Type: "image", URL: "https://example.test/image.png"}},
	}
	if perr := policy.validateTaskSend(valid); perr != nil {
		t.Fatalf("valid send rejected: %v", perr)
	}
	cases := []protocol.TaskSendParams{
		{SessionID: "s_1"},
		{Prompt: "hello"},
		{SessionID: "s_1", Prompt: "x", Attachments: []protocol.Attachment{{Type: "pdf", URL: "https://example.test/a.pdf"}}},
		{SessionID: "s_1", Prompt: "x", Attachments: []protocol.Attachment{{Type: "image", Path: "relative.png"}}},
		{SessionID: "s_1", Prompt: "x", Attachments: []protocol.Attachment{{Type: "image", URL: "file:///tmp/a.png"}}},
		{SessionID: "s_1", Prompt: "x", Attachments: []protocol.Attachment{{Type: "image", Path: "/tmp/a.png", URL: "https://example.test/a.png"}}},
	}
	for i, params := range cases {
		if perr := policy.validateTaskSend(params); perr == nil {
			t.Fatalf("case %d should be rejected", i)
		}
	}

	many := protocol.TaskSendParams{SessionID: "s_1", Prompt: "x"}
	for range 17 {
		many.Attachments = append(many.Attachments, protocol.Attachment{Type: "image", URL: "https://example.test/a.png"})
	}
	if perr := policy.validateTaskSend(many); perr == nil {
		t.Fatal("too many attachments should be rejected")
	}
}

func TestPolicyValidateApprovalReply(t *testing.T) {
	policy := newPolicyEngine()
	if perr := policy.validateApprovalReply(protocol.ApprovalReplyParams{
		SessionID: "s_1", ApprovalID: "ap_1", Decision: protocol.DecisionDeny,
	}); perr != nil {
		t.Fatalf("valid approval rejected: %v", perr)
	}
	if perr := policy.validateApprovalReply(protocol.ApprovalReplyParams{
		SessionID: "s_1", ApprovalID: "ap_1", Decision: "maybe",
	}); perr == nil {
		t.Fatal("invalid decision should be rejected")
	}
}
