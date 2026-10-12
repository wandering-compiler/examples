package business

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/wandering-compiler/sdk/go/lib/principal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentpb "github.com/wandering-compiler/examples/assistant/w17/stubs/pb/domains/assistant/agent"
	authpb "github.com/wandering-compiler/examples/assistant/w17/stubs/pb/domains/assistant/auth"
	notespb "github.com/wandering-compiler/examples/assistant/w17/stubs/pb/domains/assistant/notes"
)

// instructions is what the model is told before every question. The model
// itself is the agent plugin's default_model: a caller cannot pick one.
const instructions = "You answer questions from the person who keeps these notes. Be brief."

// researchTool is the one tool Research offers the model: a search of the
// caller's notes, which this service runs itself.
var researchTool = &agentpb.ToolSpec{
	Name:         "search_notes",
	Purpose:      "Search the asker's notes by words in the title or body.",
	Activity:     "Searching your notes",
	ParamsSchema: `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`,
}

type assistant struct {
	notespb.UnimplementedAssistantServer

	agent  agentpb.AgentServiceClient
	usage  agentpb.UsageQueryClient
	scopes agentpb.UsageMutationClient
	notes  notespb.NoteQueryClient
}

// caller is the signed-in account. Every model call is recorded against it,
// so its spend can be reported and capped.
func caller(ctx context.Context) (string, error) {
	id, err := principal.UserID(ctx, &authpb.AuthResp{})
	if err != nil {
		return "", status.Error(codes.Unauthenticated, "sign in to ask the assistant")
	}
	return id, nil
}

func question(text string) []*agentpb.Message {
	return []*agentpb.Message{{Role: agentpb.Role_ROLE_USER, Text: text}}
}

func (a *assistant) Ask(ctx context.Context, req *notespb.AskReq) (*notespb.AskResp, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	out, err := a.agent.Complete(ctx, &agentpb.CompleteReq{
		Instructions: instructions,
		Messages:     question(req.GetQuestion()),
		UsageScope:   who,
	})
	if err != nil {
		return nil, err
	}
	return &notespb.AskResp{
		Answer:       out.GetText(),
		InputTokens:  out.GetUsage().GetInputTokens(),
		OutputTokens: out.GetUsage().GetOutputTokens(),
	}, nil
}

func (a *assistant) AskStream(req *notespb.AskReq, out notespb.Assistant_AskStreamServer) error {
	ctx := out.Context()
	who, err := caller(ctx)
	if err != nil {
		return err
	}
	stream, err := a.agent.CompleteStream(ctx, &agentpb.CompleteReq{
		Instructions: instructions,
		Messages:     question(req.GetQuestion()),
		UsageScope:   who,
	})
	if err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case ev.GetDelta() != nil:
			err = out.Send(&notespb.AskChunk{Text: ev.GetDelta().GetText()})
		case ev.GetFinished() != nil:
			u := ev.GetFinished().GetUsage()
			err = out.Send(&notespb.AskChunk{Done: true, InputTokens: u.GetInputTokens(), OutputTokens: u.GetOutputTokens()})
		}
		if err != nil {
			return err
		}
	}
}

// Research runs the model with one tool, search_notes. The agent plugin
// drives the loop and asks this service to run each call the model makes;
// the search runs as the caller, so it can only ever see the caller's notes.
func (a *assistant) Research(ctx context.Context, req *notespb.AskReq) (*notespb.ResearchResp, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	run, err := a.agent.RunAgent(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = run.CloseSend() }()
	if err := run.Send(&agentpb.RunAgentReq{Msg: &agentpb.RunAgentReq_Start_{Start: &agentpb.RunAgentReq_Start{
		Instructions: instructions + " Search the notes before you answer.",
		Messages:     question(req.GetQuestion()),
		Tools:        []*agentpb.ToolSpec{researchTool},
		Limits:       &agentpb.Limits{MaxTurns: 4, MaxToolCalls: 4},
		UsageScope:   who,
	}}}); err != nil {
		return nil, err
	}
	resp := &notespb.ResearchResp{}
	for {
		ev, err := run.Recv()
		if err != nil {
			return nil, err
		}
		switch {
		case ev.GetToolCall() != nil:
			call := ev.GetToolCall()
			result, failed, query := a.searchNotes(ctx, call)
			resp.Searched = append(resp.Searched, query)
			if err := run.Send(&agentpb.RunAgentReq{Msg: &agentpb.RunAgentReq_ToolResult_{ToolResult: &agentpb.RunAgentReq_ToolResult{
				CallId: call.GetCallId(), Result: result, Failed: failed,
			}}}); err != nil {
				return nil, err
			}
		case ev.GetFinished() != nil:
			resp.Answer = ev.GetFinished().GetText()
			return resp, nil
		}
	}
}

// searchNotes runs one search_notes call. A bad call is answered to the
// MODEL as a failed tool result, not to the caller as an error: the model can
// correct itself on the next turn.
func (a *assistant) searchNotes(ctx context.Context, call *agentpb.ToolCall) (result string, failed bool, query string) {
	if call.GetName() != researchTool.GetName() {
		return "unknown tool " + call.GetName(), true, ""
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(call.GetArguments()), &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return `arguments must be {"query": "<words>"}`, true, ""
	}
	query = strings.TrimSpace(args.Query)
	found, err := a.notes.SearchNotes(ctx, &notespb.SearchNotesReq{Pattern: "%" + likeEscape(query) + "%", Limit: 5})
	if err != nil {
		return "the search failed", true, query
	}
	type note struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	notes := make([]note, 0, len(found.GetNotes()))
	for _, n := range found.GetNotes() {
		notes = append(notes, note{Title: n.GetTitle(), Body: n.GetBody()})
	}
	b, _ := json.Marshal(notes)
	return string(b), false, query
}

// likeEscape makes the caller's words literal inside a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (a *assistant) MySpend(ctx context.Context, _ *notespb.MySpendReq) (*notespb.MySpendResp, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := a.scopes.InternScope(ctx, &agentpb.InternScopeReq{ExternalId: who})
	if err != nil {
		return nil, err
	}
	spend, err := a.usage.GetScopeSpend(ctx, &agentpb.GetScopeSpendReq{
		ScopeId: scope.GetId(),
		From:    timestamppb.New(time.Unix(0, 0)),
		To:      timestamppb.New(time.Now().Add(time.Minute)),
	})
	if err != nil {
		return nil, err
	}
	out := &notespb.MySpendResp{}
	for _, l := range spend.GetLines() {
		out.Models = append(out.Models, &notespb.ModelSpend{
			Model: l.GetModel(), Calls: l.GetCalls(),
			InputTokens: l.GetInputTokens(), OutputTokens: l.GetOutputTokens(),
		})
	}
	return out, nil
}
