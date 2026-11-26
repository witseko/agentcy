package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

// workerNotReady is the answer to a launch while the fresh-run worker reconciles the
// runs of an earlier daemon at start, as in Python.
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
	_, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		_, err := launchAuthority(ctx, tx, request.Token)
		return wire.Value{}, err
	})
	if err != nil {
		return core.Outcome{}, err
	}
	result, err := handler.pluginNames()
	return core.Outcome{Result: result}, err
}

// launch binds a requested worker to this checkout, queues the committed assignment and
// hands its join code to the worker (Core.launch in core.py).
func (handler *Handler) launch(ctx context.Context, request *wire.Launch, now int64) (core.Outcome, error) {
	_, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		_, err := launchAuthority(ctx, tx, request.Token)
		return wire.Value{}, err
	})
	if err != nil {
		return core.Outcome{}, err
	}
	if !handler.freshRunsReady() {
		return core.Outcome{}, reject(workerNotReady)
	}
	directory, err := core.CheckLaunchCheckout(request.Directory, request.Workspace, handler.workspaceKeys.Key)
	if err != nil {
		return core.Outcome{}, err
	}
	plugin, err := handler.configuredPlugin(request.Plugin)
	if err != nil {
		return core.Outcome{}, err
	}
	var queued queuedLaunch
	_, err = handler.writeResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := launchAuthority(ctx, tx, request.Token)
		if err != nil {
			return wire.Value{}, err
		}
		queued, err = queueAssignment(ctx, tx, member, queueRequest{
			workspace: request.Workspace, name: request.Name, plugin: request.Plugin,
			timeout: plugin.TimeoutSeconds, model: plugin.Model, mcpTask: request.McpTask,
			relaunch: request.Relaunch,
		}, now)
		return wire.Value{}, err
	})
	if err != nil {
		return core.Outcome{}, err
	}
	return handler.dispatch(request.Workspace, directory, plugin, queued), nil
}

// operatorLaunch queues a fresh run for the human through the same dispatch path as a
// lead's launch (Core.operator_launch). The caller has resolved the checkout's workspace
// and checked that its store exists.
func (handler *Handler) operatorLaunch(ctx context.Context, workspace string, request *wire.OperatorLaunch, now int64) (core.Outcome, error) {
	if !handler.freshRunsReady() {
		return core.Outcome{}, reject(workerNotReady)
	}
	directory, err := paths.RealPath(request.Directory, true)
	if err != nil {
		return core.Outcome{}, err
	}
	plugin, err := handler.configuredPlugin(request.Plugin)
	if err != nil {
		return core.Outcome{}, err
	}
	var queued queuedLaunch
	_, err = handler.writeResult(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		var queueErr error
		queued, queueErr = operatorQueue(ctx, tx, request, queueRequest{
			workspace: workspace, name: request.Name, plugin: request.Plugin,
			timeout: plugin.TimeoutSeconds, model: plugin.Model, relaunch: request.Relaunch,
		}, now)
		return wire.Value{}, queueErr
	})
	if err != nil {
		return core.Outcome{}, err
	}
	return handler.dispatch(workspace, directory, plugin, queued), nil
}

// dispatch hands a newly committed assignment and its join code to the worker and wakes
// the room. A repeated launch of an existing assignment dispatches nothing (Core.dispatch).
func (handler *Handler) dispatch(workspace, directory string, plugin *fresh.Plugin, queued queuedLaunch) core.Outcome {
	outcome := core.Outcome{Result: queued.result}
	if queued.code == "" {
		return outcome
	}
	assignmentID, _ := queued.result.Get("assignment_id")
	handler.freshRuns.Dispatch(fresh.Dispatch{
		Workspace: workspace,
		Plugin:    *plugin,
		Start: fresh.StartArguments{
			AssignmentID: assignmentID.Text, Workspace: directory, BridgeHome: handler.stores.DataDir(),
			JoinCode: queued.code, Model: plugin.Model, TimeoutSeconds: plugin.TimeoutSeconds,
			TokenCap: plugin.TokenCap, CostCapMicroUSD: plugin.CostCapMicroUSD,
		},
	})
	outcome.Notify = []core.Room{{Workspace: workspace, Conversation: queued.room}}
	return outcome
}

// freshRunsReady reports whether the worker accepts launches. A handler without a
// worker never does.
func (handler *Handler) freshRunsReady() bool {
	return handler.freshRuns != nil && handler.freshRuns.Ready()
}

// wakeFreshRuns tells the worker that a write to the workspace may have stopped a run.
func (handler *Handler) wakeFreshRuns(workspace string) {
	if handler.freshRuns != nil {
		handler.freshRuns.Wake(workspace)
	}
}

// storeExists reports whether a workspace has a store, without creating one.
func (handler *Handler) storeExists(workspace string) bool {
	return handler.stores.Exists(workspace)
}
