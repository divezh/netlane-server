// Package node is the agent of a VPN location that takes its users from main.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/divezh/netlane-server/internal/api"
	"github.com/divezh/netlane-server/internal/store"
)

// Join registers this node with main using the invitation link from the panel
// (https://<main>/<prefix>/node/join#<code>); it returns the node id and its token.
func Join(ctx context.Context, client *http.Client, joinURL string, j store.NodeJoin) (string, string, error) {
	u, err := url.Parse(joinURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Fragment == "" {
		return "", "", errors.New("ссылка подключения должна быть вида https://…/node/join#код")
	}
	code := u.Fragment
	u.Fragment = ""
	body, _ := json.Marshal(struct {
		Code string `json:"code"`
		store.NodeJoin
	}{code, j})
	payload, err := api.EncodePayload(body)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set(api.PayloadHeader, payload)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", errors.New("главный сервер недоступен: " + err.Error())
	}
	defer resp.Body.Close()
	var out struct {
		NodeID    string `json:"node_id"`
		NodeToken string `json:"node_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out) != nil || out.NodeToken == "" {
		return "", "", errors.New("код недействителен или истёк, создайте новый в панели («Ноды → Добавить ноду»)")
	}
	return out.NodeID, out.NodeToken, nil
}
