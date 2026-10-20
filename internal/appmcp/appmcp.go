// Package appmcp exposes the app over the Model Context Protocol. Each tool
// drives the same Endpoint the matching REST operation runs
// (server.BuildViewModel: Interact + Build) and wraps the view model in its
// own output in place of Render, so a rule lives in one place whichever
// surface calls it.
//
// The caller is already authenticated by the time a tool runs (the /mcp/rpc
// route requires a credential), so a tool reads who it is acting as from
// the context with auth.KeyID, and never takes it as an argument.
package appmcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apigen "github.com/KevinMcHugh/app-template/internal/api/gen"
	"github.com/KevinMcHugh/app-template/internal/app"
	"github.com/KevinMcHugh/app-template/internal/server"
	"github.com/KevinMcHugh/app-template/internal/server/me"
	"github.com/KevinMcHugh/app-template/internal/server/notes"
)

// Instructions is sent to every client on connect. Tell the model what the
// app is for and the usual order of tools; keep it short.
const Instructions = `This server stores notes: a title and a free-text body.

Use list_notes to see what exists and get_note for one in full. create_note, update_note and delete_note change them. Confirm with the user before deleting anything.`

// NewServer builds the MCP server with every tool registered.
func NewServer(store server.Store, links app.Links) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: app.Name, Version: "0.1.0"},
		&mcpsdk.ServerOptions{Instructions: Instructions})
	registerRead(s, store, links)
	registerWrite(s, store, links)
	return s
}

// ---- inputs and outputs -----------------------------------------------------

type Empty struct{}

type MeOutput struct {
	Me me.Me `json:"me"`
}

type ListNotesOutput struct {
	Notes []notes.Note `json:"notes"`
}

type NoteIDInput struct {
	ID string `json:"id" jsonschema:"note id"`
}

type NoteOutput struct {
	Note notes.Note `json:"note"`
}

type CreateNoteInput struct {
	Title string `json:"title" jsonschema:"a short title, up to 200 characters"`
	Body  string `json:"body,omitempty" jsonschema:"the note's text, up to 20000 characters"`
}

type UpdateNoteInput struct {
	ID    string  `json:"id" jsonschema:"note id"`
	Title *string `json:"title,omitempty" jsonschema:"new title; omit to keep it"`
	Body  *string `json:"body,omitempty" jsonschema:"new body; omit to keep it"`
}

type OKOutput struct {
	OK bool `json:"ok"`
}

// ---- registration -----------------------------------------------------------

func registerRead(s *mcpsdk.Server, store server.Store, links app.Links) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "whoami",
		Description: "The api key this connection acts as.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ Empty) (*mcpsdk.CallToolResult, MeOutput, error) {
		vm, err := server.BuildViewModel(ctx, me.Endpoint{Store: store}, apigen.GetMeRequestObject{})
		return nil, MeOutput{Me: vm}, err
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_notes",
		Description: "List every note, newest first.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ Empty) (*mcpsdk.CallToolResult, ListNotesOutput, error) {
		vm, err := server.BuildViewModel(ctx, notes.ListEndpoint{Store: store, Links: links}, apigen.ListNotesRequestObject{})
		return nil, ListNotesOutput{Notes: vm}, err
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "get_note",
		Description: "One note in full.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in NoteIDInput) (*mcpsdk.CallToolResult, NoteOutput, error) {
		vm, err := server.BuildViewModel(ctx, notes.GetEndpoint{Store: store, Links: links}, apigen.GetNoteRequestObject{NoteId: in.ID})
		return nil, NoteOutput{Note: vm}, err
	})
}

func registerWrite(s *mcpsdk.Server, store server.Store, links app.Links) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "create_note",
		Description: "Save a new note.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in CreateNoteInput) (*mcpsdk.CallToolResult, NoteOutput, error) {
		vm, err := server.BuildViewModel(ctx, notes.CreateEndpoint{Store: store, Links: links}, apigen.CreateNoteRequestObject{
			Body: &apigen.NoteCreate{Title: in.Title, Body: in.Body},
		})
		return nil, NoteOutput{Note: vm}, err
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "update_note",
		Description: "Change a note's title, body, or both. Fields left out stay as they are.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in UpdateNoteInput) (*mcpsdk.CallToolResult, NoteOutput, error) {
		vm, err := server.BuildViewModel(ctx, notes.UpdateEndpoint{Store: store, Links: links}, apigen.UpdateNoteRequestObject{
			NoteId: in.ID,
			Body:   &apigen.NoteUpdate{Title: in.Title, Body: in.Body},
		})
		return nil, NoteOutput{Note: vm}, err
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "delete_note",
		Description: "Delete a note. Confirm with the user first.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in NoteIDInput) (*mcpsdk.CallToolResult, OKOutput, error) {
		ok, err := server.BuildViewModel(ctx, notes.DeleteEndpoint{Store: store}, apigen.DeleteNoteRequestObject{NoteId: in.ID})
		return nil, OKOutput{OK: ok}, err
	})
}
