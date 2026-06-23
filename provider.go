package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const webhookContentType = "application/external.dns.webhook+json;version=1"

type provider struct {
	cfg         config
	authMu      sync.Mutex
	authCookie  string
	authExpires time.Time
}

type filtersResponse struct {
	Filters []string `json:"filters"`
}

type endpoint struct {
	DNSName          string                     `json:"dnsName"`
	Targets          []string                   `json:"targets,omitempty"`
	RecordType       string                     `json:"recordType"`
	SetIdentifier    string                     `json:"setIdentifier,omitempty"`
	RecordTTL        int64                      `json:"recordTTL,omitempty"`
	Labels           map[string]string          `json:"labels,omitempty"`
	ProviderSpecific []providerSpecificProperty `json:"providerSpecific,omitempty"`
}

type providerSpecificProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type changes struct {
	Create    []endpoint `json:"create"`
	UpdateOld []endpoint `json:"updateOld"`
	UpdateNew []endpoint `json:"updateNew"`
	Delete    []endpoint `json:"delete"`
}

func (p *provider) negotiate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := p.authedClient(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filters, err := p.filters(ctx, client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, filtersResponse{Filters: filters})
}

func (p *provider) records(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p.getRecords(w, r)
	case http.MethodPost:
		p.applyChanges(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

func (p *provider) adjustEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	var eps []endpoint
	if err := decodeJSON(r, &eps); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, eps)
}

func (p *provider) getRecords(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := p.authedClient(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	projectID, err := p.projectID(ctx, client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	zones, err := p.zones(ctx, client, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var out []endpoint
	for _, zone := range zones {
		rrsets, err := client.GetRRsets(ctx, projectID, zone.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, rr := range rrsets {
			out = append(out, endpoint{
				DNSName:    absoluteRecordName(rr.Name, zone.Domain),
				RecordType: strings.ToUpper(rr.Type),
				RecordTTL:  int64(rr.TTL),
				Targets:    append([]string(nil), rr.Records...),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DNSName == out[j].DNSName {
			return out[i].RecordType < out[j].RecordType
		}
		return out[i].DNSName < out[j].DNSName
	})
	writeJSON(w, http.StatusOK, out)
}

func (p *provider) applyChanges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client, err := p.authedClient(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var ch changes
	if err := decodeJSON(r, &ch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	for _, ep := range ch.Delete {
		if err := p.deleteEndpoint(ctx, client, ep); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	for _, ep := range ch.UpdateOld {
		if err := p.deleteEndpoint(ctx, client, ep); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	for _, ep := range ch.Create {
		if err := p.upsertEndpoint(ctx, client, ep); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	for _, ep := range ch.UpdateNew {
		if err := p.upsertEndpoint(ctx, client, ep); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (p *provider) authedClient(ctx context.Context) (*wxOneClient, error) {
	p.authMu.Lock()
	defer p.authMu.Unlock()

	if p.authCookie != "" && time.Now().Before(p.authExpires) {
		client := newWxOneClient(p.cfg.Host, p.cfg.Tenant)
		client.cookie = p.authCookie
		return client, nil
	}

	client := newWxOneClient(p.cfg.Host, p.cfg.Tenant)
	if err := client.Login(ctx, p.cfg.Username, p.cfg.Password); err != nil {
		return nil, err
	}
	p.authCookie = client.cookie
	p.authExpires = time.Now().Add(p.cfg.AuthCacheTTL)
	return client, nil
}

func (p *provider) filters(ctx context.Context, client *wxOneClient) ([]string, error) {
	if len(p.cfg.Filters) > 0 {
		return prefixDots(p.cfg.Filters), nil
	}
	projectID, err := p.projectID(ctx, client)
	if err != nil {
		return nil, err
	}
	zones, err := p.zones(ctx, client, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		out = append(out, "."+strings.TrimSpace(z.Domain))
	}
	return out, nil
}

func (p *provider) projectID(ctx context.Context, client *wxOneClient) (string, error) {
	if p.cfg.ProjectID != "" {
		return p.cfg.ProjectID, nil
	}
	proj, err := client.GetDefaultProject(ctx)
	if err != nil {
		return "", err
	}
	return proj.ID, nil
}

func (p *provider) zones(ctx context.Context, client *wxOneClient, projectID string) ([]domainZone, error) {
	if p.cfg.ZoneID != "" {
		zone, err := client.GetDomainZone(ctx, projectID, p.cfg.ZoneID)
		if err != nil {
			return nil, err
		}
		return []domainZone{zone}, nil
	}
	return client.GetDomainZones(ctx, projectID)
}

func (p *provider) upsertEndpoint(ctx context.Context, client *wxOneClient, ep endpoint) error {
	projectID, zone, err := p.resolveZone(ctx, client, ep.DNSName)
	if err != nil {
		return err
	}
	if len(ep.Targets) == 0 {
		return nil
	}
	ttl := int(ep.RecordTTL)
	if ttl <= 0 {
		ttl = 60
	}
	relName, err := relativeRecordName(ep.DNSName, zone.Domain)
	if err != nil {
		return err
	}
	return client.UpsertRRset(ctx, projectID, zone.ID, relName, normalizeRecordType(ep.RecordType), ttl, uniqueStrings(ep.Targets))
}

func (p *provider) deleteEndpoint(ctx context.Context, client *wxOneClient, ep endpoint) error {
	projectID, zone, err := p.resolveZone(ctx, client, ep.DNSName)
	if err != nil {
		return err
	}
	relName, err := relativeRecordName(ep.DNSName, zone.Domain)
	if err != nil {
		return err
	}
	rr, err := client.GetRRset(ctx, projectID, zone.ID, relName, normalizeRecordType(ep.RecordType))
	if err != nil {
		return err
	}
	if rr == nil || len(rr.Records) == 0 {
		return nil
	}
	if len(ep.Targets) == 0 {
		return client.DeleteRRset(ctx, projectID, zone.ID, relName, normalizeRecordType(ep.RecordType), rr.Records)
	}
	rem := subtractStrings(rr.Records, ep.Targets)
	if len(rem) == 0 {
		return client.DeleteRRset(ctx, projectID, zone.ID, relName, normalizeRecordType(ep.RecordType), rr.Records)
	}
	return client.UpsertRRset(ctx, projectID, zone.ID, relName, normalizeRecordType(ep.RecordType), rr.TTL, rem)
}

func (p *provider) resolveZone(ctx context.Context, client *wxOneClient, dnsName string) (string, domainZone, error) {
	projectID, err := p.projectID(ctx, client)
	if err != nil {
		return "", domainZone{}, err
	}
	zones, err := p.zones(ctx, client, projectID)
	if err != nil {
		return "", domainZone{}, err
	}
	zone := matchZone(dnsName, zones)
	if zone == nil {
		return "", domainZone{}, fmt.Errorf("no matching zone found for domain %s", dnsName)
	}
	return projectID, *zone, nil
}

func matchZone(dnsName string, zones []domainZone) *domainZone {
	var matched *domainZone
	for i, z := range zones {
		if z.Domain == "" {
			continue
		}
		if dnsName == z.Domain || strings.HasSuffix(dnsName, "."+z.Domain) {
			if matched == nil || len(z.Domain) > len(matched.Domain) {
				matched = &zones[i]
			}
		}
	}
	return matched
}

func relativeRecordName(dnsName, zoneDomain string) (string, error) {
	zoneDomain = strings.TrimSuffix(strings.TrimSpace(zoneDomain), ".")
	if zoneDomain == "" {
		return "", fmt.Errorf("zone domain is required")
	}
	base := strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	if base == "" {
		return "", fmt.Errorf("dns name is required")
	}
	if base == zoneDomain {
		return "", nil
	}
	suffix := "." + zoneDomain
	if !strings.HasSuffix(base, suffix) {
		return "", fmt.Errorf("%s is not under zone %s", dnsName, zoneDomain)
	}
	return strings.TrimSuffix(base, suffix), nil
}

func absoluteRecordName(name, zoneDomain string) string {
	zoneDomain = strings.TrimSuffix(strings.TrimSpace(zoneDomain), ".")
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || name == "@" {
		return zoneDomain
	}
	if name == zoneDomain || strings.HasSuffix(name, "."+zoneDomain) {
		return name
	}
	return name + "." + zoneDomain
}

func normalizeRecordType(v string) string {
	return strings.ToUpper(strings.TrimSpace(v))
}

func prefixDots(filters []string) []string {
	out := make([]string, 0, len(filters))
	for _, f := range filters {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.HasPrefix(f, ".") {
			f = "." + f
		}
		out = append(out, f)
	}
	return out
}

func uniqueStrings(v []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(v))
	for _, s := range v {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func subtractStrings(base, remove []string) []string {
	seen := map[string]struct{}{}
	for _, s := range remove {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(base))
	for _, s := range base {
		if _, ok := seen[s]; ok {
			continue
		}
		out = append(out, s)
	}
	return out
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", webhookContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	log.Printf("webhook error: status=%d error=%q", status, err)
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
