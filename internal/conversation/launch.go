package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/wire"
)

// Seams for milestone M4. A launch that passes every check below would queue an
// assignment and hand its join code to the fresh-run worker, which this milestone does
// not have. Such a launch gets workerNotReady, the answer Python gives while its worker
// cannot take runs, and nothing is written:
//
//   - member "launch": after launchAuthority, CheckLaunchCheckout and the plugin lookup
//     succeed (Core.launch in core.py, before assignments.queue);
//   - "operator_launch": after the checkout check and the plugin lookup succeed
//     (Core.operator_launch, before operator.launch).
//
// The assignment rows themselves are read already: status shows launch_state,
// HasActiveWork counts pending rows, invitations refuse a slot reserved for an active
// run, and mcp_task_status and mcp_task_cancel find runs by their task digest.
const workerNotReady = "Fresh-run worker is reconciling earlier runs. Retry shortly"

// pluginNotConfigured is the answer to a launch of a plugin that plugins.json lacks.
const pluginNotConfigured = "Fresh-run plugin is not configured"

// pluginSettings loads plugins.json; its errors reach the client as Python's do.
func (handler *Handler) pluginSettings() (*fresh.Settings, error) {
	settings, err := handler.plugins.Load()
	if err != nil {
		return nil, reject(err.Error())
	}
	return settings, nil
}

// pluginNames lists each configured plugin's name and model, sorted by name, without
// commands, environment or caps.
func (handler *Handler) pluginNames() (wire.Value, error) {
	settings, err := handler.pluginSettings()
	if err != nil {
		return wire.Value{}, err
	}
	names := []wire.Value{}
	if settings != nil {
		for _, plugin := range settings.Plugins {
			entry := wire.NewObject()
			entry.Set("name", wire.TextValue(plugin.Name))
			entry.Set("model", wire.TextValue(plugin.Model))
			names = append(names, entry)
		}
	}
	return singleField("plugins", wire.ListValue(names)), nil
}

// configuredPlugin resolves a plugin by name from plugins.json.
func (handler *Handler) configuredPlugin(name string) (*fresh.Plugin, error) {
	settings, err := handler.pluginSettings()
	if err != nil {
		return nil, err
	}
	if settings == nil || settings.Find(name) == nil {
		return nil, reject(pluginNotConfigured)
	}
	return settings.Find(name), nil
}

// memberPlugins lists the plugins for an active lead of an open conversation.
func (handler *Handler) memberPlugins(ctx context.Context, request *wire.Plugins) (core.Outcome, error) {
	_, err := handler.writeResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		_, err := launchAuthority(ctx, tx, request.Token)
		return wire.Value{}, err
	})
	if err != nil {
		return core.Outcome{}, err
	}
	result, err := handler.pluginNames()
	return core.Outcome{Result: result}, err
}

// launch checks a member's launch request up to the M4 seam described above.
func (handler *Handler) launch(ctx context.Context, request *wire.Launch) (core.Outcome, error) {
	_, err := handler.writeResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		if _, err := launchAuthority(ctx, tx, request.Token); err != nil {
			return wire.Value{}, err
		}
		if _, err := core.CheckLaunchCheckout(request.Directory, request.Workspace, handler.workspaceKeys.Key); err != nil {
			return wire.Value{}, err
		}
		if _, err := handler.configuredPlugin(request.Plugin); err != nil {
			return wire.Value{}, err
		}
		return wire.Value{}, reject(workerNotReady)
	})
	return core.Outcome{}, err
}

// operatorLaunch checks the human's launch request up to the M4 seam described above.
// The caller has resolved the checkout's workspace and checked that its store exists.
func (handler *Handler) operatorLaunch(request *wire.OperatorLaunch) (core.Outcome, error) {
	if _, err := handler.configuredPlugin(request.Plugin); err != nil {
		return core.Outcome{}, err
	}
	return core.Outcome{}, reject(workerNotReady)
}

// storeExists reports whether a workspace has a store, without creating one.
func (handler *Handler) storeExists(workspace string) bool {
	return store.Exists(handler.dataDir, workspace)
}
