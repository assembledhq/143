#!/bin/sh
# Source this file in a dedicated shell, then call:
#   recovery_watchdog_run SECONDS SIGNAL_CALLBACK START_CALLBACK COMMAND [ARGS...]
# SIGNAL_CALLBACK receives SIGNAL and the owned command PID. START_CALLBACK
# receives that PID; returning nonzero cancels the owned command and waits for
# cleanup. It does not prove that the command caused no side effects.
# The caller must wait for this function before exiting. It owns INT/TERM/HUP
# and EXIT traps until it returns; callbacks must not spawn background children.

recovery_watchdog_run() {
    recovery_wd_seconds=$1
    recovery_wd_signal=$2
    recovery_wd_started=$3
    shift 3
    recovery_wd_child=
    recovery_wd_watchdog=
    recovery_wd_interrupted=0

    recovery_wd_interrupt() {
        recovery_wd_interrupted=1
        [ -z "$recovery_wd_child" ] || "$recovery_wd_signal" TERM "$recovery_wd_child" || :
    }

    recovery_wd_cleanup() {
        trap - 0
        trap '' INT TERM HUP
        if [ -n "$recovery_wd_child" ]; then
            "$recovery_wd_signal" KILL "$recovery_wd_child" || :
            wait "$recovery_wd_child" 2>/dev/null || :
            recovery_wd_child=
        fi
        if [ -n "$recovery_wd_watchdog" ]; then
            kill -TERM "$recovery_wd_watchdog" 2>/dev/null || :
            wait "$recovery_wd_watchdog" 2>/dev/null || :
            recovery_wd_watchdog=
        fi
    }

    recovery_wd_watch() {
        recovery_wd_timer=
        recovery_wd_stopping=0
        recovery_wd_reap_timer() {
            trap '' INT TERM HUP
            if [ -n "$recovery_wd_timer" ]; then
                kill -TERM "$recovery_wd_timer" 2>/dev/null || :
                wait "$recovery_wd_timer" 2>/dev/null || :
                recovery_wd_timer=
            fi
        }
        recovery_wd_delay() {
            sleep "$1" &
            recovery_wd_timer=$!
            # The trap only records interruption. Even a signal between fork
            # and $! assignment reaches this path before the parent can exit.
            if [ "$recovery_wd_stopping" = 0 ]; then
                wait "$recovery_wd_timer" 2>/dev/null || :
            fi
            if [ "$recovery_wd_stopping" != 0 ]; then
                recovery_wd_reap_timer
                return 1
            fi
            recovery_wd_timer=
        }
        trap 'recovery_wd_stopping=1' INT TERM HUP
        trap 'recovery_wd_reap_timer' 0
        recovery_wd_delay "$recovery_wd_seconds" || exit 0
        recovery_wd_tries=0
        while [ "$recovery_wd_tries" -lt 30 ]; do
            "$recovery_wd_signal" TERM "$recovery_wd_child" || :
            recovery_wd_delay .1 || exit 0
            recovery_wd_tries=$((recovery_wd_tries + 1))
        done
        "$recovery_wd_signal" KILL "$recovery_wd_child" || :
    }

    trap 'recovery_wd_interrupt' INT TERM HUP
    trap 'recovery_wd_cleanup' 0
    # POSIX asynchronous commands can receive /dev/null on fd 0. Preserve the
    # command's input explicitly before forking, then close its extra copy.
    { "$@" <&3 3<&- & } 3<&0
    recovery_wd_child=$!
    if [ "$recovery_wd_interrupted" != 0 ] || ! "$recovery_wd_started" "$recovery_wd_child"; then
        recovery_wd_status=1
    else
        recovery_wd_watch </dev/null >/dev/null 2>&1 &
        recovery_wd_watchdog=$!
        if [ "$recovery_wd_interrupted" != 0 ]; then
            recovery_wd_status=1
        else
            if wait "$recovery_wd_child"; then
                recovery_wd_status=0
            else
                recovery_wd_status=$?
            fi
            if [ "$recovery_wd_interrupted" != 0 ]; then
                recovery_wd_status=1
            else
                recovery_wd_child=
            fi
        fi
    fi
    recovery_wd_cleanup
    trap - INT TERM HUP
    return "$recovery_wd_status"
}
