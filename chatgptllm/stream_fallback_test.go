package chatgptllm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func indexedItem(w io.Writer, kind string, index int, item map[string]any) {
	event(w, map[string]any{"type": "response.output_item." + kind, "output_index": index, "item": item})
}

func collectFallbackStream(body string) ([]llm.StreamEvent, error) {
	var events []llm.StreamEvent
	err := consume(&http.Response{Body: io.NopCloser(strings.NewReader(body)), StatusCode: http.StatusOK}, map[string]string{"probe": "probe.read"}, func(ev llm.StreamEvent, err error) bool {
		events = append(events, ev)
		return true
	})
	return events, err
}

func fallbackFinalMessage(t *testing.T, events []llm.StreamEvent) llm.Message {
	t.Helper()
	for _, ev := range events {
		if ev.Type == llm.SEMessageFinal && ev.FinalMessage != nil {
			return *ev.FinalMessage
		}
	}
	t.Fatal("missing authoritative final message")
	return llm.Message{}
}

func TestCompletedOutputFallbackPreservesPhaseAndRawFields(t *testing.T) {
	var body strings.Builder
	added := textItem("")
	added["phase"], added["status"] = "final_answer", "in_progress"
	indexedItem(&body, "added", 0, added)
	event(&body, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "output_index": 0, "delta": "OK"})
	done := textItem("OK")
	done["phase"], done["status"] = "final_answer", "completed"
	done["provider_extension"] = map[string]any{"keep": "opaque"}
	indexedItem(&body, "done", 0, done)
	// The live route emits an actual empty array, not just a missing field.
	completed(&body, []map[string]any{}...)
	events, err := collectFallbackStream(body.String())
	if err != nil {
		t.Fatal(err)
	}
	msg := fallbackFinalMessage(t, events)
	if msg.Text() != "OK" {
		t.Fatalf("text=%q", msg.Text())
	}
	items, err := replayItems(msg)
	want, _ := json.Marshal(done)
	if err != nil || len(items) != 1 || string(items[0]) != string(want) {
		t.Fatalf("raw completion lost: output=%s err=%v", items, err)
	}
	var text strings.Builder
	for _, ev := range events {
		if ev.Type == llm.SETextDelta {
			text.WriteString(ev.Text)
		}
	}
	if text.String() != "OK" {
		t.Fatalf("streamed text duplicated: %q", text.String())
	}
}

func TestCompletedOutputFallbackSortsDoneAndRetainsSummarylessReasoning(t *testing.T) {
	var body strings.Builder
	reasoning := map[string]any{"id": "rs0", "type": "reasoning", "summary": []any{}, "encrypted_content": "opaque-state"}
	a := callItem("a", "call-a", "probe", "")
	b := callItem("b", "call-b", "probe", "")
	indexedItem(&body, "added", 0, map[string]any{"id": "rs0", "type": "reasoning"})
	indexedItem(&body, "added", 1, a)
	indexedItem(&body, "added", 2, b)
	for _, delta := range []struct{ id, text string }{{"a", `{"n":`}, {"b", `{"n":`}, {"a", `1}`}, {"b", `2}`}} {
		event(&body, map[string]any{"type": "response.function_call_arguments.delta", "item_id": delta.id, "delta": delta.text})
	}
	a["arguments"], b["arguments"] = `{"n":1}`, `{"n":2}`
	indexedItem(&body, "done", 2, b)
	indexedItem(&body, "done", 1, a)
	indexedItem(&body, "done", 0, reasoning)
	completed(&body)
	events, err := collectFallbackStream(body.String())
	if err != nil {
		t.Fatal(err)
	}
	var calls, arguments []string
	for _, ev := range events {
		switch ev.Type {
		case llm.SEToolUseStart:
			calls = append(calls, ev.ToolID)
		case llm.SEToolInputJSON:
			arguments = append(arguments, ev.Text)
		case llm.SEThinkingDelta:
			t.Fatal("summaryless reasoning became visible")
		}
	}
	if !reflect.DeepEqual(calls, []string{"call-a", "call-b"}) || !reflect.DeepEqual(arguments, []string{`{"n":1}`, `{"n":2}`}) {
		t.Fatalf("wrong tool ordering: calls=%v args=%v", calls, arguments)
	}
	msg := fallbackFinalMessage(t, events)
	items, err := replayItems(msg)
	if err != nil || len(items) != 3 {
		t.Fatalf("invalid replay output: %s err=%v", items, err)
	}
	for i, want := range []map[string]any{reasoning, a, b} {
		raw, _ := json.Marshal(want)
		if string(items[i]) != string(raw) {
			t.Fatalf("output[%d]=%s want=%s", i, items[i], raw)
		}
	}
	for _, block := range msg.Content {
		if block.Type == llm.BlockThinking || block.Signature != "" {
			t.Fatal("opaque reasoning escaped into visible content")
		}
	}
}

func TestCompletedOutputFallbackRejectsUntrustedItems(t *testing.T) {
	for name, write := range map[string]func(io.Writer){
		"missing done": func(w io.Writer) { indexedItem(w, "added", 0, callItem("a", "call-a", "probe", `{}`)) },
		"missing both": func(io.Writer) {},
		"missing index": func(w io.Writer) {
			item := callItem("a", "call-a", "probe", `{}`)
			event(w, map[string]any{"type": "response.output_item.added", "item": item})
			event(w, map[string]any{"type": "response.output_item.done", "item": item})
		},
		"unknown done ID": func(w io.Writer) {
			indexedItem(w, "added", 0, callItem("a", "call-a", "probe", `{}`))
			indexedItem(w, "done", 0, callItem("b", "call-a", "probe", `{}`))
		},
		"duplicate done": func(w io.Writer) {
			item := callItem("a", "call-a", "probe", `{}`)
			indexedItem(w, "added", 0, item)
			indexedItem(w, "done", 0, item)
			indexedItem(w, "done", 0, item)
		},
		"changed index": func(w io.Writer) {
			item := callItem("a", "call-a", "probe", `{}`)
			indexedItem(w, "added", 0, item)
			indexedItem(w, "done", 1, item)
		},
		"duplicate index": func(w io.Writer) {
			indexedItem(w, "added", 0, callItem("a", "call-a", "probe", `{}`))
			indexedItem(w, "added", 0, callItem("b", "call-b", "probe", `{}`))
		},
		"negative index": func(w io.Writer) { indexedItem(w, "added", -1, callItem("a", "call-a", "probe", `{}`)) },
		"index gap": func(w io.Writer) {
			item := callItem("a", "call-a", "probe", `{}`)
			indexedItem(w, "added", 1, item)
			indexedItem(w, "done", 1, item)
		},
		"changed tool identity": func(w io.Writer) {
			indexedItem(w, "added", 0, callItem("a", "call-a", "probe", `{}`))
			indexedItem(w, "done", 0, callItem("a", "call-b", "probe", `{}`))
		},
		"changed arguments": func(w io.Writer) {
			indexedItem(w, "added", 0, callItem("a", "call-a", "probe", ""))
			event(w, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "a", "delta": `{"n":1}`})
			indexedItem(w, "done", 0, callItem("a", "call-a", "probe", `{"n":2}`))
		},
		"changed text": func(w io.Writer) {
			indexedItem(w, "added", 0, textItem(""))
			event(w, map[string]any{"type": "response.output_text.delta", "item_id": "msg1", "delta": "first"})
			indexedItem(w, "done", 0, textItem("different"))
		},
		"changed phase": func(w io.Writer) {
			item := textItem("")
			item["phase"] = "commentary"
			indexedItem(w, "added", 0, item)
			item["phase"] = "final_answer"
			indexedItem(w, "done", 0, item)
		},
		"incomplete done": func(w io.Writer) {
			item := callItem("a", "call-a", "probe", `{}`)
			indexedItem(w, "added", 0, item)
			item["status"] = "in_progress"
			indexedItem(w, "done", 0, item)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var body strings.Builder
			write(&body)
			completed(&body)
			events, err := collectFallbackStream(body.String())
			assertFallbackRejected(t, events, err)
		})
	}
}

func TestCompletedOutputFallbackRequiresTerminalCompletion(t *testing.T) {
	for _, terminal := range []string{"response.incomplete", "response.failed", "response.completed", "EOF", "DONE"} {
		t.Run(terminal, func(t *testing.T) {
			var body strings.Builder
			item := callItem("a", "call-a", "probe", `{}`)
			indexedItem(&body, "added", 0, item)
			indexedItem(&body, "done", 0, item)
			switch terminal {
			case "EOF":
			case "DONE":
				body.WriteString("data: [DONE]\n\n")
			default:
				event(&body, map[string]any{"type": terminal, "response": map[string]any{"status": "incomplete", "output": []any{}}})
			}
			events, err := collectFallbackStream(body.String())
			assertFallbackRejected(t, events, err)
		})
	}
}

func TestNonemptyCompletedOutputRemainsAuthoritative(t *testing.T) {
	for _, mode := range []string{"missing added item", "conflicting completed item", "incomplete final item"} {
		t.Run(mode, func(t *testing.T) {
			var body strings.Builder
			item := callItem("a", "call-a", "probe", `{}`)
			indexedItem(&body, "added", 0, item)
			indexedItem(&body, "done", 0, item)
			switch mode {
			case "missing added item":
				completed(&body, textItem("must not replace missing tool"))
			case "conflicting completed item":
				item["arguments"] = `{"changed":true}`
				completed(&body, item)
			case "incomplete final item":
				item["status"] = "incomplete"
				completed(&body, item)
			}
			events, err := collectFallbackStream(body.String())
			assertFallbackRejected(t, events, err)
		})
	}
	// No indexes or done events are needed when the final output is complete.
	var body strings.Builder
	item := callItem("a", "call-a", "probe", `{}`)
	event(&body, map[string]any{"type": "response.output_item.added", "item": item})
	completed(&body, item)
	events, err := collectFallbackStream(body.String())
	if err != nil || len(fallbackFinalMessage(t, events).Content) != 1 {
		t.Fatalf("legacy authoritative completion rejected: %v", err)
	}
}

func assertFallbackRejected(t *testing.T, events []llm.StreamEvent, err error) {
	t.Helper()
	var providerErr *Error
	if !errors.As(err, &providerErr) || !providerErr.NoFallback() {
		t.Fatalf("invalid stream accepted: %v", err)
	}
	for _, ev := range events {
		if ev.Type == llm.SEToolUseStart || ev.Type == llm.SEToolInputJSON || ev.Type == llm.SEMessageFinal || ev.Type == llm.SEMessageStop {
			t.Fatalf("unsafe output released before valid completion: %+v", ev)
		}
	}
}
