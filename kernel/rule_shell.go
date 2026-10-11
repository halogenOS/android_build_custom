// Copyright (C) 2026 The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

package kernel

import (
	"path/filepath"
	"strings"

	"android/soong/android"

	"github.com/google/blueprint/proptools"
)

// shq quotes a word for bash.
func shq(s string) string {
	return proptools.ShellEscapeIncludingSpaces(s)
}

// treeWord gives a bash word for a path that Soong gives relative to the top of the source tree
// or as an absolute path. A relative path is prefixed with $T, which the scripts set to the tree
// top, the working directory of every build step, so that the word holds from any working
// directory.
func treeWord(p string) string {
	if filepath.IsAbs(p) {
		return shq(p)
	}
	return `"$T"/` + shq(p)
}

// words joins bash words with spaces.
func words(w ...string) string {
	return strings.Join(w, " ")
}

// shellRule is a rule without a sandbox whose commands write files at fixed paths of its module's
// out directory. Its step's shell runs every command in one inner bash, started under env -i with
// the rule's environment through setsid, so the inner bash and every process it starts belong to a
// session and process group of their own, which a stopped step can end as a whole.
type shellRule struct {
	// name names the lock file <module out>/<name>.lock, the process group file
	// <module out>/<name>.pgid and the rule's TMPDIR <module out>/tmp/<name>.
	name string
	// modOut is the module's out directory.
	modOut string
	// toolDir puts <module out>/tools first on PATH.
	toolDir bool
	// clangDir is the clang directory whose bin is on PATH.
	clangDir string
	// env holds further VAR=value words of the environment, quoted.
	env []string
	// body is the inner script after the lock is held and the directories are made.
	body string
}

// build writes the inner script to <module out>/<name>.sh with a write-file rule and adds the step's
// shell, which runs that script, as a command of rule, with the script as an input; the caller adds
// the rule's other inputs and outputs to the returned command. A Ninja rule command is one line, and
// the newlines of a script given in it would be removed, so the script reaches the shell as a file.
func (r shellRule) build(ctx android.ModuleContext, rule *android.RuleBuilder) *android.RuleBuilderCommand {
	script := android.PathForModuleOut(ctx, r.name+".sh")
	android.WriteFileRuleVerbatim(ctx, script, r.innerScript())
	return rule.Command().Text(r.command(script.String())).Implicit(script)
}

// command is the step's shell: it reads the inner script from the file script whole, so that a
// later build rewriting the file in place cannot change the script of a run that still reads it,
// starts the inner bash with it in the background, forwards INT and TERM to the new session's
// process group, waits for the inner bash and exits with its status.
func (r shellRule) command(script string) string {
	path := []string{}
	if r.toolDir {
		path = append(path, treeWord(r.modOut+"/tools"))
	}
	path = append(path, treeWord(r.clangDir+"/bin"), treeWord(buildToolsPath), `"$PATH"`)
	env := []string{
		"PATH=" + strings.Join(path, ":"),
		"HOME=" + treeWord(r.modOut+"/home"),
		"TMPDIR=" + treeWord(r.modOut+"/tmp/"+r.name),
		"LC_ALL=C",
		"TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0='*'",
	}
	env = append(env, r.env...)
	return "{ T=$PWD; script=$(cat " + treeWord(script) + ") || exit $?; env -i " + strings.Join(env, " ") +
		" " + treeWord(buildToolsPath+"/setsid") + ` bash -c "$script"` +
		" & p=$!; trap 'kill -TERM -$p 2>/dev/null' INT TERM; " +
		"while :; do wait $p; s=$?; kill -0 $p 2>/dev/null || break; done; exit $s; }"
}

// innerScript is the preamble that takes the rule's lock, followed by the body.
func (r shellRule) innerScript() string {
	var b strings.Builder
	b.WriteString("T=$PWD\n")
	b.WriteString("MOD=" + treeWord(r.modOut) + "\n")
	b.WriteString("NAME=" + shq(r.name) + "\n")
	b.WriteString("TOYBOX=" + treeWord(buildToolsBin+"/toybox") + "\n")
	b.WriteString(sessionPreamble)
	dirs := `"$HOME"`
	if r.toolDir {
		dirs += ` "$MOD/tools"`
	}
	b.WriteString("mkdir -p " + dirs + " && rm -rf \"$TMPDIR\" && mkdir -p \"$TMPDIR\" || exit $?\n")
	b.WriteString("START=$(date +%s.%N) || exit $?\n")
	b.WriteString(shellFunctions)
	b.WriteString(r.body)
	return b.String()
}

// sessionPreamble takes the lock <module out>/<name>.lock with the flock of the toybox that TOYBOX
// names, the one of the build tools, on a file descriptor that every process of the run inherits,
// before it starts any other process. While a process of an earlier run holds it, the preamble
// sends KILL to the process group that <name>.pgid names, reading the file again before each try,
// once per group and only when a process of that group holds the lock file open, and tries again
// once a second, at most 60 times. The processes of a group are those whose /proc/<pid>/stat names
// it as their process group, the third field after the command name: the toybox pgrep's -g matches
// group IDs, not process groups. Holding the lock, it writes the process group of the inner bash,
// read from /proc/$$/stat since $$ need not lead the group when bash runs behind soong_ui's path
// interposer, to <name>.pgid.
const sessionPreamble = `LOCK="$MOD/$NAME.lock"
PGIDF="$MOD/$NAME.pgid"
read -r stat < /proc/$$/stat
set -- ${stat##*) }
PGID=$3
SID=$4
set --
exec 9>>"$LOCK" || exit $?
group_procs() {
  local g=$1 p st
  for p in /proc/[0-9]*; do
    read -r st < "$p/stat" 2>/dev/null || continue
    set -- ${st##*) }
    [ "$3" = "$g" ] && echo "${p#/proc/}"
  done
  return 0
}
holds_lock() {
  local p fd
  for p in $(group_procs "$1"); do
    for fd in /proc/$p/fd/*; do
      [ "$(readlink "$fd" 2>/dev/null)" = "$lockreal" ] && return 0
    done
  done
  return 1
}
if ! "$TOYBOX" flock -n 9; then
  lockreal=$(realpath "$LOCK")
  tries=0
  killed=" "
  while :; do
    g=$(cat "$PGIDF" 2>/dev/null)
    case "$g" in
      ''|*[!0-9]*|0) ;;
      *) case "$killed" in
           *" $g "*) ;;
           *) if holds_lock "$g"; then kill -KILL -"$g" 2>/dev/null; killed="$killed$g "; fi ;;
         esac ;;
    esac
    sleep 1
    tries=$((tries + 1))
    "$TOYBOX" flock -n 9 && break
    if [ $tries -ge 60 ]; then
      echo "$NAME: the lock $LOCK is still held after $tries tries; the processes of process group ${g:-?}, which did not end on KILL:" >&2
      case "$g" in
        ''|*[!0-9]*|0) ;;
        *) for p in $(group_procs "$g"); do
             read -r ps < /proc/$p/stat 2>/dev/null || continue
             set -- ${ps##*) }
             echo "  process $p, state $1" >&2
           done ;;
      esac
      exit 1
    fi
  done
fi
echo "$PGID" > "$PGIDF" || exit $?
`

// shellFunctions are the functions every inner script uses.
//
// die ends the run with a status and a message, after failure_hook, which gets the status. The
// shared failure_hook does nothing; a rule body defines its own after these functions when it
// has something to undo on a failure, as the kbuild body does for its marker.
//
// session_has_others reports whether a process of the rule's session is left besides the inner
// bash and the processes it descends from within the session, for a hook that must know whether
// anything may still be writing.
//
// stage puts a file in place at an output path: an equal destination keeps its modification time,
// and otherwise the file is renamed over it from <destination>.tmp, so the destination is always
// either the complete old file or the complete new one.
//
// write_depfile writes a depfile with kbuild_input_check's depfile mode to <depfile>.tmp and
// renames it over the depfile.
//
// check_unchanged fails when an input changed while the rule ran: a file or directory below the
// given paths whose change time is later than the time the run started, Python bytecode caches
// left out.
const shellFunctions = `failure_hook() { :; }
session_has_others() {
  local chain=" $$ " p=$$ pp st
  while :; do
    read -r st < /proc/$p/stat 2>/dev/null || break
    set -- ${st##*) }
    pp=$2
    [ "$pp" -gt 1 ] 2>/dev/null || break
    read -r st < /proc/$pp/stat 2>/dev/null || break
    set -- ${st##*) }
    [ "$4" = "$SID" ] || break
    chain="$chain$pp "
    p=$pp
  done
  for p in $(pgrep -s "$SID"); do
    case "$chain" in *" $p "*) continue ;; esac
    [ -d /proc/$p ] && return 0
  done
  return 1
}
die() {
  local st=$1
  shift
  [ $# -gt 0 ] && echo "$NAME: $*" >&2
  failure_hook "$st"
  exit "$st"
}
stage() {
  if [ -e "$2" ] && cmp -s "$1" "$2"; then
    if [ "$1" = "$2.tmp" ]; then rm -f "$1"; fi
    return 0
  fi
  if [ "$1" != "$2.tmp" ]; then cp "$1" "$2.tmp" || return; fi
  mv -f "$2.tmp" "$2"
}
write_depfile() {
  local d=$1 t=$2 st
  shift 2
  "$KIC" depfile -o "$d.tmp" -t "$t" "$@"
  st=$?
  if [ $st -ne 0 ]; then
    echo "$NAME: writing the depfile $d failed: the walk of its entries failed, and the next build walks them again" >&2
    return $st
  fi
  mv -f "$d.tmp" "$d"
}
check_unchanged() {
  local changed
  changed=$(find "$@" \( -name __pycache__ -prune \) -o \( -name '*.pyc' \) -o \( -newerct "@$START" -print \) 2>/dev/null)
  if [ -n "$changed" ]; then
    echo "$NAME: these inputs changed while the rule ran, so the next build runs it again:" >&2
    echo "$changed" | sed 's/^/  /' >&2
    return 1
  fi
}
`
