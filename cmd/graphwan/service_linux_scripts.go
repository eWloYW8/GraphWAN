//go:build linux || darwin || windows

package main

import (
	"strings"

	"github.com/kardianos/service"
)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func configureLinuxScripts(config *service.Config, helper bool) {
	args := make([]string, len(config.Arguments))
	for i, arg := range config.Arguments {
		args[i] = shellQuote(arg)
	}
	// Insert quoted data into the template, never into template source. Use an
	// explicit start function to avoid openrc-run's second shell evaluation.
	config.Option["OpenRCScript"] = graphwanOpenRCScript
	config.Option["SysvScript"] = graphwanProcdScript
	config.Option["ShellExecutable"] = shellQuote(config.Executable)
	config.Option["ShellArguments"] = strings.Join(args, " ")
	config.Option["ShellDirectory"] = shellQuote(config.WorkingDirectory)
	config.Option["OneShotHelper"] = helper
}

const graphwanOpenRCScript = `#!/sbin/openrc-run
description="GraphWAN service"
pidfile="/run/{{.Name}}.pid"
{{if not (index .Option "OneShotHelper")}}supervisor=supervise-daemon
{{end}}
depend() {
	need net
	after firewall
}

start() {
	ebegin "Starting {{.Name}}"
{{if index .Option "OneShotHelper"}}
	start-stop-daemon --start --background --make-pidfile \
		--pidfile "$pidfile" --umask 0077 \
		--chdir {{index .Option "ShellDirectory"}} \
		--exec {{index .Option "ShellExecutable"}} \
		-- {{index .Option "ShellArguments"}}
{{else}}
	supervise-daemon "$RC_SVCNAME" --start --pidfile "$pidfile" \
		--respawn-delay 3 --respawn-max 0 --retry TERM/30/KILL/5 \
		--umask 0077 --chdir {{index .Option "ShellDirectory"}} \
		--stdout "/var/log/{{.Name}}.log" --stderr "/var/log/{{.Name}}.err" \
		{{index .Option "ShellExecutable"}} -- {{index .Option "ShellArguments"}}
{{end}}
	eend $?
}
{{if index .Option "OneShotHelper"}}
stop() {
	ebegin "Stopping {{.Name}}"
	start-stop-daemon --stop --pidfile "$pidfile" --retry TERM/120/KILL/5
	eend $?
}
{{else}}
status() {
	supervise_status || return $?
	# A live supervisor does not prove the worker survived startup. In
	# particular, updates must roll back a binary that exits immediately.
	local child="$(service_get_value child_pid)"
	case "$child" in
		''|*[!0-9]*) eerror "status: no worker"; return 1 ;;
	esac
	if ! kill -0 "$child" 2>/dev/null || grep -q '^State:.*Z' "/proc/$child/status" 2>/dev/null; then
		eerror "status: worker not running"
		return 1
	fi
}
{{end}}
`

// procd has no working-directory/umask instance parameters. The shell sets
// these and then execs GraphWAN; procd supervises GraphWAN's PID directly.
const graphwanProcdScript = `#!/bin/sh /etc/rc.common
USE_PROCD=1
START=50
STOP=02

start_service() {
	procd_open_instance
	procd_set_param command /bin/sh -c 'cd "$1" && shift && umask 0077 && exec "$@"' graphwan \
		{{index .Option "ShellDirectory"}} {{index .Option "ShellExecutable"}} {{index .Option "ShellArguments"}}
{{if not (index .Option "OneShotHelper")}}
	procd_set_param respawn 3600 3 0
	procd_set_param term_timeout 30
{{else}}
	procd_set_param term_timeout 120
{{end}}
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_set_param pidfile /var/run/{{.Name}}.pid
	procd_close_instance
}

status_service() {
	if procd_running {{.Name}}; then
		echo running
	else
		echo inactive
		return 3
	fi
}
`
