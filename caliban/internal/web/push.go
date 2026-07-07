package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/levmv/golems/caliban/internal/store"
)

const (
	pushPayloadTTL                = 24 * time.Hour
	scheduledTurnPushBodyMaxRunes = 180
	scheduledTurnPushNotification = "\U0001F44B"
	reminderPushNotification      = "\u23f0"
)

// PushConfig enables standards-based Web Push for installed PWAs.
type PushConfig struct {
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	Subject         string
}

func (c PushConfig) enabled() bool {
	return strings.TrimSpace(c.VAPIDPublicKey) != "" &&
		strings.TrimSpace(c.VAPIDPrivateKey) != "" &&
		strings.TrimSpace(c.Subject) != ""
}

type pushSender func(ctx context.Context, sub store.PushSubscription, payload pushPayload) (int, error)

type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag,omitempty"`
	URL   string `json:"url,omitempty"`
}

type pushConfigResponse struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"publicKey,omitempty"`
}

type pushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type deletePushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
}

func defaultPushSender(cfg PushConfig) pushSender {
	if !cfg.enabled() {
		return nil
	}
	return func(ctx context.Context, sub store.PushSubscription, payload pushPayload) (int, error) {
		body, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		resp, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.P256DH,
				Auth:   sub.Auth,
			},
		}, &webpush.Options{
			Subscriber:      vapidSubject(cfg.Subject),
			VAPIDPublicKey:  strings.TrimSpace(cfg.VAPIDPublicKey),
			VAPIDPrivateKey: strings.TrimSpace(cfg.VAPIDPrivateKey),
			TTL:             int(pushPayloadTTL.Seconds()),
			Urgency:         webpush.UrgencyNormal,
		})
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
}

func vapidSubject(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimPrefix(s, "mailto:")
}

func (t *Transport) pushConfig(w http.ResponseWriter, r *http.Request) {
	enabled := t.push.enabled() && t.store != nil
	resp := pushConfigResponse{Enabled: enabled}
	if enabled {
		resp.PublicKey = strings.TrimSpace(t.push.VAPIDPublicKey)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (t *Transport) savePushSubscription(w http.ResponseWriter, r *http.Request) {
	if !t.push.enabled() || t.store == nil {
		writeError(w, http.StatusNotFound, "web push is not enabled")
		return
	}
	resolved, ok := t.resolveChat(r.Context(), r.PathValue("chatId"))
	if !ok {
		writeError(w, http.StatusNotFound, "chat not found")
		return
	}
	var req pushSubscriptionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ps := store.PushSubscription{
		Endpoint:       req.Endpoint,
		ConversationID: resolved.ConversationID,
		P256DH:         req.Keys.P256DH,
		Auth:           req.Keys.Auth,
		UserAgent:      r.UserAgent(),
	}
	if err := t.store.UpsertPushSubscription(r.Context(), ps); err != nil {
		t.logf("web: save push subscription: %v", err)
		writeError(w, http.StatusBadRequest, "invalid push subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (t *Transport) deletePushSubscription(w http.ResponseWriter, r *http.Request) {
	if t.store == nil {
		writeError(w, http.StatusNotFound, "chat storage is not enabled")
		return
	}
	var req deletePushSubscriptionRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req)
	if strings.TrimSpace(req.Endpoint) == "" {
		req.Endpoint = r.URL.Query().Get("endpoint")
	}
	if strings.TrimSpace(req.Endpoint) == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	if _, err := t.store.DeletePushSubscription(r.Context(), req.Endpoint); err != nil {
		t.logf("web: delete push subscription: %v", err)
		writeError(w, http.StatusInternalServerError, "could not delete push subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Notify sends a Web Push notification to subscriptions bound to conversationID.
// It is deliberately best-effort: a web push send failure must not make reminder
// delivery retry and duplicate notifications through other transports.
func (t *Transport) Notify(ctx context.Context, conversationID int64, text string) error {
	payload := pushPayload{
		Title: reminderPushNotification,
		Body:  pushNotificationBody(text),
		Tag:   fmt.Sprintf("caliban-%d-%d", conversationID, time.Now().UnixNano()),
		URL:   "/",
	}
	return t.sendPushPayload(ctx, conversationID, payload)
}

// NotifyScheduledTurn sends a short Web Push attention signal after a scheduled
// agent turn finishes. The full answer remains in the chat; the push body is
// only a compact preview.
func (t *Transport) NotifyScheduledTurn(ctx context.Context, conversationID int64, reply string) error {
	payload := pushPayload{
		Title: scheduledTurnPushNotification,
		Body:  pushNotificationPreview(reply),
		Tag:   fmt.Sprintf("caliban-scheduled-%d-%d", conversationID, time.Now().UnixNano()),
		URL:   "/",
	}
	return t.sendPushPayload(ctx, conversationID, payload)
}

func (t *Transport) sendPushPayload(ctx context.Context, conversationID int64, payload pushPayload) error {
	if !t.push.enabled() || t.store == nil || t.sendPush == nil {
		return nil
	}
	subs, err := t.store.PushSubscriptions(ctx, conversationID)
	if err != nil {
		t.logf("web: list push subscriptions: %v", err)
		return nil
	}
	for _, sub := range subs {
		status, err := t.sendPush(ctx, sub, payload)
		if err != nil {
			t.logf("web: send push to %s: %v", sub.Endpoint, err)
			continue
		}
		if status >= 200 && status < 300 {
			continue
		}
		if status == http.StatusGone || status == http.StatusNotFound {
			if _, err := t.store.DeletePushSubscription(ctx, sub.Endpoint); err != nil {
				t.logf("web: delete expired push subscription: %v", err)
			}
			continue
		}
		t.logf("web: push service returned status %d for %s", status, sub.Endpoint)
	}
	return nil
}

func pushNotificationBody(text string) string {
	body := strings.TrimSpace(text)
	body = strings.TrimPrefix(body, reminderPushNotification)
	return strings.TrimSpace(body)
}

func pushNotificationPreview(text string) string {
	body := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(body)
	if len(runes) <= scheduledTurnPushBodyMaxRunes {
		return body
	}
	if scheduledTurnPushBodyMaxRunes <= 3 {
		return string(runes[:scheduledTurnPushBodyMaxRunes])
	}
	return strings.TrimSpace(string(runes[:scheduledTurnPushBodyMaxRunes-3])) + "..."
}
