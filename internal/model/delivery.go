package model

// Delivery converts canonical state into wire-ready payloads. Every raw BBCode
// field is rendered here, once, at the delivery boundary, so the renderer's
// cache and its escape fallback are exercised in exactly one place. Sessions
// and the core each hold one backed by the same renderer; the type itself is
// stateless, so concurrent use is safe when the underlying Renderer is.
type Delivery struct {
	r Renderer
}

// NewDelivery returns a Delivery backed by r. A nil renderer yields escaped
// plain text, matching the Render* helpers.
func NewDelivery(r Renderer) Delivery { return Delivery{r: r} }

// EntryHTML renders one stored entry's body through the kind-aware path.
func (d Delivery) EntryHTML(kind, body string, data []byte) string {
	return RenderEntryHTML(d.r, kind, body, data)
}

// EntryUncachedHTML is EntryHTML through the uncached path, for one-off
// artifacts (warpmark snippets, chatlog exports).
func (d Delivery) EntryUncachedHTML(kind, body string, data []byte) string {
	return RenderEntryUncachedHTML(d.r, kind, body, data)
}

// Entry renders one stored entry into its delivery form.
func (d Delivery) Entry(e Entry) RenderedEntry {
	return RenderedEntry{Entry: e, HTML: d.EntryHTML(e.Kind, e.Body, e.Data)}
}

// Window renders a slice of stored entries into delivery entries.
func (d Delivery) Window(entries []Entry) []RenderedEntry {
	out := make([]RenderedEntry, len(entries))
	for i, e := range entries {
		out[i] = d.Entry(e)
	}
	return out
}

// Status renders one raw BBCode status message or description.
func (d Delivery) Status(msg string) string { return RenderHTML(d.r, msg) }

// Message renders one chat message body (applying the emote convention).
func (d Delivery) Message(msg string) string { return RenderMessageHTML(d.r, msg) }

// Presence renders a roster entry's status message for delivery.
func (d Delivery) Presence(p PresencePayload) PresencePayload {
	p.StatusMsg = d.Status(p.StatusMsg)
	return p
}

// Member renders a member row's status message for delivery.
func (d Delivery) Member(m MemberInfo) MemberInfo {
	m.StatusMsg = d.Status(m.StatusMsg)
	return m
}

// Members renders a slice of member rows for delivery.
func (d Delivery) Members(in []MemberInfo) []MemberInfo {
	if len(in) == 0 {
		return in
	}
	out := make([]MemberInfo, len(in))
	for i, m := range in {
		out[i] = d.Member(m)
	}
	return out
}

// ConversationState renders a conversation state payload's description for
// delivery. A nil Description is left alone: it is the sparse "unchanged"
// signal the client honors by keeping its own copy.
func (d Delivery) ConversationState(p ConvStatePayload) ConvStatePayload {
	if p.Description != nil {
		rendered := d.Status(*p.Description)
		p.Description = &rendered
	}
	return p
}
