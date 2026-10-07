package proxy

import (
	"context"
	stdjson "encoding/json"
	"fmt"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

// Start runs an embedded sing-box instance that exposes one loopback mixed
// inbound and forwards all traffic through the supplied outbounds. Only
// traffic forwarding is allowed: sections that could open listeners or alter
// the host (API services, TUN endpoints, clash/v2ray/debug controllers) are
// dropped.
func Start(ctx context.Context, config []byte, port int) (*box.Box, error) {
	ctx = include.Context(ctx)
	var raw map[string]any
	if err := stdjson.Unmarshal(config, &raw); err != nil {
		return nil, fmt.Errorf("invalid proxy configuration")
	}
	outbounds, ok := raw["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return nil, fmt.Errorf("proxy configuration must contain at least one outbound")
	}
	delete(raw, "services")
	delete(raw, "endpoints")
	delete(raw, "experimental")
	raw["log"] = map[string]any{"disabled": true}
	raw["inbounds"] = []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}
	data, err := stdjson.Marshal(raw)
	if err != nil {
		return nil, err
	}
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return nil, fmt.Errorf("invalid embedded sing-box options")
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return nil, fmt.Errorf("initialize embedded sing-box: %w", err)
	}
	if err = instance.Start(); err != nil {
		instance.Close()
		return nil, fmt.Errorf("start embedded sing-box: %w", err)
	}
	return instance, nil
}
