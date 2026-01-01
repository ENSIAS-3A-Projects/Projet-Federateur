package agent

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML embed.FS

// DashboardHandler serves the monitoring dashboard.
type DashboardHandler struct {
	agent *Agent
}

// NewDashboardHandler creates a dashboard handler.
func NewDashboardHandler(agent *Agent) *DashboardHandler {
	return &DashboardHandler{agent: agent}
}

func (d *DashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(dashboardHTML, "dashboard.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
