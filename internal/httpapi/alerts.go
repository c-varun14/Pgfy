package httpapi

import (
	"net/http"
	"time"

	"github.com/c-varun14/Pgfy/internal/alerts"
)

func (s *Server) alertsReady(w http.ResponseWriter) bool {
	if s.Alerts == nil {
		failure(w, 503, "metadata_unavailable", "Alerts are unavailable on this installation. Run host diagnostics.")
		return false
	}
	return true
}

func (s *Server) getAlertSettings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.alertsReady(w) {
		return
	}
	hook, configured, e := s.Alerts.Webhook(r.Context())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Alert settings could not be read.")
		return
	}
	// The URL may itself be the credential; only its scheme and host leave the server.
	write(w, 200, map[string]any{"configured": configured, "url": hook.Masked(), "has_secret": hook.Secret != "", "private_endpoint": hook.PrivateEndpoint})
}

func (s *Server) putAlertSettings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.alertsReady(w) {
		return
	}
	var in alerts.Webhook
	if !decode(w, r, &in) {
		return
	}
	// A stored webhook that can no longer be read (for example after a key
	// change) must still be replaceable: treat it as absent.
	current, configured, e := s.Alerts.Webhook(r.Context())
	if e != nil {
		configured = false
	}
	if in.URL == "" {
		// An empty URL turns alerts off.
		in = alerts.Webhook{}
	} else {
		if configured && in.URL == current.Masked() {
			// The masked form came back unchanged: keep the stored URL.
			in.URL = current.URL
		}
		if in.Secret == "" && configured {
			in.Secret = current.Secret
		}
		if e := in.Validate(); e != nil {
			failure(w, 400, "invalid_webhook", e.Error())
			return
		}
	}
	if e := s.Alerts.SaveWebhook(r.Context(), in); e != nil {
		failure(w, 503, "metadata_unavailable", "Alert settings could not be saved.")
		return
	}
	s.audit(w, r, "settings.alerts", in.Host(), map[string]bool{"enabled": in.URL != "", "private_endpoint": in.PrivateEndpoint})
	s.Alerts.Kick()
	write(w, 200, map[string]any{"configured": in.URL != "", "url": in.Masked(), "has_secret": in.Secret != "", "private_endpoint": in.PrivateEndpoint})
}

func (s *Server) testAlerts(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.alertsReady(w) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	if e := s.Alerts.SendTest(ctx); e != nil {
		write(w, 200, map[string]any{"ok": false, "error": e.Error()})
		return
	}
	write(w, 200, map[string]any{"ok": true})
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.alertsReady(w) {
		return
	}
	conditions, e := s.Store.AlertConditions(r.Context())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Alerts could not be read.")
		return
	}
	lastOK, lastError := s.Alerts.Delivery()
	write(w, 200, map[string]any{"conditions": conditions, "delivery": map[string]any{"last_ok_at": lastOK, "last_error": lastError}})
}
