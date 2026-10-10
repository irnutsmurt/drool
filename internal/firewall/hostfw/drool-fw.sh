#!/bin/sh
# drool-fw: DROOL host firewall tool (DESIGN §17). Installed and updated by
# DROOL; do not edit. It owns only:
#   - the nftables table "inet drool"
#   - /var/lib/drool/firewall (journal, committed and candidate policy)
#   - drool-firewall.service (boot loader) and transient drool-fw-rollback-*
#     units (the dead-man timer)
# It never runs "nft flush ruleset" and never touches /etc/nftables.conf or
# any other table.
#
# Output protocol: "@@key value" lines; errors are "@@ERR code message" and
# a non-zero exit. Paths can be redirected for tests with DROOL_FW_STATE,
# DROOL_FW_SELF and DROOL_FW_LOCK.
set -u
umask 077
TOOL_VERSION=2
STATE=${DROOL_FW_STATE:-/var/lib/drool/firewall}
SELF=${DROOL_FW_SELF:-/usr/libexec/drool-firewall/drool-fw}
LOCK=${DROOL_FW_LOCK:-/run/drool-fw.lock}
J=$STATE/transaction.json
C=$STATE/committed.json
EMPTY='table inet drool
delete table inet drool
'

err() { printf '@@ERR %s %s\n' "$1" "$2"; exit "${3:-1}"; }
kv() { printf '@@%s %s\n' "$1" "$2"; }
now() { date +%s; }
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ; }
sha() { sha256sum "$1" | cut -c1-64; }
clean() { printf '%s' "$1" | tr -c 'A-Za-z0-9 _.:/,()=+-' ' ' | cut -c1-300; }

# durable FILE: stdin → temp file → fsync → rename → fsync directory.
durable() {
	d=$(dirname "$1")
	t=$(mktemp "$d/.tmp.XXXXXXXXXX") || return 1
	if ! cat >"$t"; then rm -f "$t"; return 1; fi
	chmod 600 "$t"
	if ! sync "$t"; then rm -f "$t"; return 1; fi
	if ! mv -f "$t" "$1"; then rm -f "$t"; return 1; fi
	sync "$d"
}

# jget FILE KEY: a value from one of our JSON files (one key per line).
jget() {
	[ -f "$1" ] || return 0
	sed -n "s/^ *\"$2\": *\"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\} *\$/\1/p" "$1" | head -n 1
}

lock() {
	exec 9>"$LOCK" || err lock "cannot open $LOCK"
	flock -w 60 9 || err busy "another drool-fw run holds the lock"
}

livehash() {
	if nft list table inet drool >/dev/null 2>&1; then
		nft -s list table inet drool 2>/dev/null | sha256sum | cut -c1-64
	else
		echo none
	fi
}

# journal STATE TX PREV CAND EPOCH REV DEADLINE REASON [SETS_GEN RULES_HASH]
journal() {
	durable "$J" <<EOF
{
  "transaction_id": "$2",
  "state": "$1",
  "previous_hash": "sha256:$3",
  "candidate_hash": "sha256:$4",
  "state_version": {
    "domain": "firewall",
    "controller_epoch": "$5",
    "revision": ${6:-0}
  },
  "deadline": "$(iso "${7:-0}")",
  "deadline_unix": ${7:-0},
  "sets_generation": ${9:-0},
  "rules_hash": "${10:-}",
  "reason": "$(clean "$8")",
  "updated": "$(iso "$(now)")"
}
EOF
}

# committed_meta HASH REV EPOCH TX LIVE SETS_GEN RULES_HASH
committed_meta() {
	durable "$C" <<EOF
{
  "hash": "$1",
  "revision": ${2:-0},
  "controller_epoch": "$3",
  "transaction_id": "$4",
  "live_hash": "$5",
  "sets_generation": ${6:-0},
  "rules_hash": "$7",
  "committed": "$(iso "$(now)")"
}
EOF
}

jhash() { jget "$J" "$1" | sed 's/^sha256://'; }

stop_timer() {
	[ -n "$1" ] || return 0
	systemctl stop "drool-fw-rollback-$1.timer" >/dev/null 2>&1 || true
}

# restore REASON: load committed.nft (the last committed policy), mark the
# pending transaction rolled back. Idempotent; the caller holds the lock.
restore() {
	tx=$(jget "$J" transaction_id)
	out=$(nft -f "$STATE/committed.nft" 2>&1)
	rc=$?
	if [ $rc -ne 0 ]; then
		sleep 1
		out=$(nft -f "$STATE/committed.nft" 2>&1)
		rc=$?
	fi
	reason=$1
	if [ $rc -ne 0 ]; then
		# Last resort: never leave the candidate in place. Removing our own
		# table restores the host's behaviour without DROOL.
		printf '%s' "$EMPTY" | nft -f - >/dev/null 2>&1
		reason="$reason; restoring the committed policy failed ($(clean "$out")); DROOL's table was removed"
	fi
	journal rolled_back "$tx" "$(jhash previous_hash)" "$(jhash candidate_hash)" \
		"$(jget "$J" controller_epoch)" "$(jget "$J" revision)" "$(jget "$J" deadline_unix)" "$reason" \
		"$(jget "$J" sets_generation)" "$(jget "$J" rules_hash)"
	rm -f "$STATE/candidate.nft"
	stop_timer "$tx"
	kv state rolled_back
	kv reason "$(clean "$reason")"
}

ensure_state() {
	mkdir -p "$STATE" || err state "cannot create $STATE"
	chmod 700 "$STATE"
}

seen() { [ -f "$STATE/seen" ] && grep -qx "$1" "$STATE/seen"; }
mark_seen() {
	{ [ -f "$STATE/seen" ] && tail -n 999 "$STATE/seen"; echo "$1"; } | durable "$STATE/seen.new" &&
		mv -f "$STATE/seen.new" "$STATE/seen" && sync "$STATE"
}

# ---------------------------------------------------------------------------

cmd_apply() {
	tx= epoch= rev= hash= timeout= expires= allow_drift=0 setsgen=0 rules=
	while [ $# -gt 0 ]; do
		case $1 in
		--tx) tx=$2; shift ;;
		--epoch) epoch=$2; shift ;;
		--revision) rev=$2; shift ;;
		--hash) hash=$2; shift ;;
		--timeout) timeout=$2; shift ;;
		--expires) expires=$2; shift ;;
		--sets-generation) setsgen=$2; shift ;;
		--rules-hash) rules=$2; shift ;;
		--allow-drift) allow_drift=1 ;;
		*) err usage "unknown argument $1" ;;
		esac
		shift
	done
	case $tx in fwtx_[a-z0-9]*) ;; *) err usage "bad transaction id" ;; esac
	case $epoch in *[!0-9a-f]* | '') err usage "bad epoch" ;; esac
	case $rev$timeout$expires$setsgen in *[!0-9]*) err usage "bad number" ;; esac
	[ -n "$rev" ] && [ -n "$timeout" ] && [ -n "$expires" ] || err usage "missing argument"
	[ "$timeout" -ge 10 ] || err usage "the rollback timeout must be at least 10 seconds"
	ensure_state
	t=$(mktemp "$STATE/.in.XXXXXXXXXX") || err state "cannot write $STATE"
	cat >"$t"
	lock
	[ "$(now)" -le "$expires" ] || { rm -f "$t"; err expired "the command expired at $(iso "$expires")"; }
	seen "$tx" && { rm -f "$t"; err duplicate "transaction $tx was already received; not applying it again"; }
	st=$(jget "$J" state)
	if [ "$st" = pending ]; then
		dl=$(jget "$J" deadline_unix)
		if [ "$(now)" -lt "${dl:-0}" ]; then
			rm -f "$t"
			err pending "transaction $(jget "$J" transaction_id) is pending until $(jget "$J" deadline)"
		fi
		restore "its deadline passed (found by the next transaction)" >/dev/null
	fi
	if [ -f "$C" ]; then
		cep=$(jget "$C" controller_epoch)
		crev=$(jget "$C" revision)
		if [ -n "$cep" ] && [ "$cep" != "$epoch" ] && [ "${crev:-0}" -gt 0 ]; then
			rm -f "$t"
			err epoch "this host's firewall state belongs to controller epoch $cep, the command to $epoch; it is never applied automatically"
		fi
		if [ "$rev" -le "${crev:-0}" ]; then
			rm -f "$t"
			err stale "stale firewall configuration: revision $rev, this host already committed revision $crev"
		fi
	fi
	sync "$t" && mv -f "$t" "$STATE/candidate.nft" && sync "$STATE" || err state "cannot write the candidate"
	h=$(sha "$STATE/candidate.nft")
	[ "$h" = "$hash" ] || { rm -f "$STATE/candidate.nft"; err hash "the candidate's hash $h does not match $hash"; }
	if ! out=$(nft -c -f "$STATE/candidate.nft" 2>&1); then
		rm -f "$STATE/candidate.nft"
		err invalid "nftables rejected the candidate: $(clean "$out")"
	fi
	live=$(livehash)
	if [ ! -f "$STATE/committed.nft" ]; then
		if [ "$live" != none ] && [ $allow_drift -eq 0 ]; then
			rm -f "$STATE/candidate.nft"
			err drift "a DROOL table exists but this host has no committed DROOL policy"
		fi
		printf '%s' "$EMPTY" | durable "$STATE/committed.nft" || err state "cannot write committed.nft"
		committed_meta "$(sha "$STATE/committed.nft")" 0 "$epoch" "" none 0 ""
	elif [ "$live" != "$(jget "$C" live_hash)" ] && [ $allow_drift -eq 0 ]; then
		rm -f "$STATE/candidate.nft"
		err drift "DROOL's live rules differ from the last committed policy (changed outside DROOL)"
	fi
	prev=$(sha "$STATE/committed.nft")
	dl=$(($(now) + timeout))
	# The journal is durable BEFORE the live rules change.
	journal pending "$tx" "$prev" "$h" "$epoch" "$rev" "$dl" "" "$setsgen" "$rules" || err state "cannot write the journal"
	mark_seen "$tx"
	if ! out=$(systemd-run --quiet --unit="drool-fw-rollback-$tx" --description="DROOL firewall rollback ($tx)" \
		--on-active="${timeout}s" --timer-property=AccuracySec=1s "$SELF" rollback --tx "$tx" --reason timeout 2>&1); then
		journal aborted "$tx" "$prev" "$h" "$epoch" "$rev" "$dl" "could not arm the rollback timer" "$setsgen" "$rules"
		rm -f "$STATE/candidate.nft"
		err timer "could not arm the rollback timer: $(clean "$out")"
	fi
	if ! out=$(nft -f "$STATE/candidate.nft" 2>&1); then
		restore "applying the candidate failed: $out" >/dev/null
		err apply "applying the candidate failed (nothing changed): $(clean "$out")"
	fi
	kv state pending
	kv transaction_id "$tx"
	kv deadline_unix "$dl"
	kv previous_hash "$prev"
	kv candidate_hash "$h"
}

cmd_commit() {
	tx= hash=
	while [ $# -gt 0 ]; do
		case $1 in
		--tx) tx=$2; shift ;;
		--hash) hash=$2; shift ;;
		*) err usage "unknown argument $1" ;;
		esac
		shift
	done
	[ -n "$tx" ] && [ -n "$hash" ] || err usage "missing argument"
	lock
	jtx=$(jget "$J" transaction_id)
	st=$(jget "$J" state)
	[ "$jtx" = "$tx" ] || err unknown_tx "transaction $tx is not this host's current transaction (current: ${jtx:-none} $st)"
	case $st in
	committed) kv state committed; exit 0 ;;
	pending) ;;
	*) err "$st" "transaction $tx is $st: $(jget "$J" reason)" ;;
	esac
	[ "$(jhash candidate_hash)" = "$hash" ] || err hash "the pending candidate's hash does not match"
	if [ "$(now)" -gt "$(jget "$J" deadline_unix)" ]; then
		restore "the commit arrived after the deadline" >/dev/null
		err deadline "the commit arrived after the deadline; the previous policy was restored"
	fi
	durable "$STATE/committed.nft" <"$STATE/candidate.nft" || err state "cannot write committed.nft"
	committed_meta "$hash" "$(jget "$J" revision)" "$(jget "$J" controller_epoch)" "$tx" "$(livehash)" \
		"$(jget "$J" sets_generation)" "$(jget "$J" rules_hash)" || err state "cannot write committed.json"
	journal committed "$tx" "$(jhash previous_hash)" "$hash" "$(jget "$J" controller_epoch)" "$(jget "$J" revision)" \
		"$(jget "$J" deadline_unix)" "" "$(jget "$J" sets_generation)" "$(jget "$J" rules_hash)" || err state "cannot write the journal"
	rm -f "$STATE/candidate.nft"
	stop_timer "$tx"
	kv state committed
	kv revision "$(jget "$C" revision)"
}

cmd_rollback() {
	tx= reason="rolled back on request"
	while [ $# -gt 0 ]; do
		case $1 in
		--tx) tx=$2; shift ;;
		--reason) reason=$2; shift ;;
		*) err usage "unknown argument $1" ;;
		esac
		shift
	done
	[ "$reason" = timeout ] && reason="no commit before the deadline (the rollback timer fired)"
	lock
	st=$(jget "$J" state)
	if [ "$st" = pending ] && { [ -z "$tx" ] || [ "$tx" = "$(jget "$J" transaction_id)" ]; }; then
		restore "$reason"
		exit 0
	fi
	kv state "${st:-none}"
}

cmd_boot() {
	lock
	[ -f "$STATE/committed.nft" ] || { kv state none; exit 0; }
	if [ "$(jget "$J" state)" = pending ]; then
		# Never the candidate: a reboot during a transaction rolls it back.
		restore "the host rebooted during the transaction"
		exit 0
	fi
	if ! out=$(nft -f "$STATE/committed.nft" 2>&1); then
		echo "drool-fw: loading the committed policy failed: $out" >&2
		err boot "loading the committed policy failed: $(clean "$out")"
	fi
	committed_meta "$(jget "$C" hash)" "$(jget "$C" revision)" "$(jget "$C" controller_epoch)" \
		"$(jget "$C" transaction_id)" "$(livehash)" "$(jget "$C" sets_generation)" "$(jget "$C" rules_hash)"
	kv state loaded
}

# sets: apply a set-element delta (fd 3) and record the new full committed
# policy (fd 4). Not a dead-man transaction (DESIGN §15.4): rules don't change.
cmd_sets() {
	rules= gen= newhash=
	while [ $# -gt 0 ]; do
		case $1 in
		--rules-hash) rules=$2; shift ;;
		--generation) gen=$2; shift ;;
		--hash) newhash=$2; shift ;;
		*) err usage "unknown argument $1" ;;
		esac
		shift
	done
	case $gen in *[!0-9]* | '') err usage "bad generation" ;; esac
	[ -n "$rules" ] && [ -n "$newhash" ] || err usage "missing argument"
	ensure_state
	d=$(mktemp "$STATE/.delta.XXXXXXXXXX") && n=$(mktemp "$STATE/.next.XXXXXXXXXX") || err state "cannot write $STATE"
	cat <&3 >"$d"
	cat <&4 >"$n"
	lock
	[ "$(jget "$J" state)" != pending ] || { rm -f "$d" "$n"; err pending "a firewall transaction is pending"; }
	[ -f "$C" ] && [ "$(jget "$C" rules_hash)" = "$rules" ] || { rm -f "$d" "$n"; err base "the committed rules are not the ones these sets belong to"; }
	cur=$(jget "$C" sets_generation)
	if [ "${cur:-0}" -ge "$gen" ]; then rm -f "$d" "$n"; kv state committed; kv sets_generation "$cur"; exit 0; fi
	[ "$(livehash)" = "$(jget "$C" live_hash)" ] || { rm -f "$d" "$n"; err drift "DROOL's live rules differ from the last committed policy (changed outside DROOL)"; }
	[ "$(sha "$n")" = "$newhash" ] || { rm -f "$d" "$n"; err hash "the new policy's hash does not match"; }
	if ! out=$(nft -c -f "$d" 2>&1); then rm -f "$d" "$n"; err invalid "nftables rejected the set update: $(clean "$out")"; fi
	sync "$n"
	if ! out=$(nft -f "$d" 2>&1); then rm -f "$d" "$n"; err apply "applying the set update failed (nothing changed): $(clean "$out")"; fi
	rm -f "$d"
	mv -f "$n" "$STATE/committed.nft" && sync "$STATE" || err state "cannot write committed.nft"
	committed_meta "$newhash" "$(jget "$C" revision)" "$(jget "$C" controller_epoch)" "$(jget "$C" transaction_id)" \
		"$(livehash)" "$gen" "$rules"
	kv state committed
	kv sets_generation "$gen"
}

# rebase: move the committed policy to a new controller epoch (a controller
# restored from a backup starts a new one, DESIGN §10.3). Nothing about the
# rules changes. Only when no transaction is pending and the committed state
# is exactly the one the controller compared (epoch, revision, hash); done
# twice, the second run changes nothing. Old-epoch commands are refused from
# then on.
cmd_rebase() {
	from= epoch= rev= hash=
	while [ $# -gt 0 ]; do
		case $1 in
		--from) from=$2; shift ;;
		--epoch) epoch=$2; shift ;;
		--revision) rev=$2; shift ;;
		--hash) hash=$2; shift ;;
		*) err usage "unknown argument $1" ;;
		esac
		shift
	done
	case $from$epoch in *[!0-9a-f]* | '') err usage "bad epoch" ;; esac
	case $rev in *[!0-9]* | '') err usage "bad revision" ;; esac
	lock
	[ "$(jget "$J" state)" != pending ] || err pending "a firewall transaction is pending; it must commit or roll back first"
	[ -f "$C" ] || err base "this host has no committed DROOL policy"
	cep=$(jget "$C" controller_epoch)
	crev=$(jget "$C" revision)
	chash=$(jget "$C" hash)
	if [ "$cep" = "$epoch" ] && [ "${crev:-0}" = "$rev" ] && [ "$chash" = "$hash" ]; then
		kv state rebased
		kv committed_epoch "$epoch"
		exit 0
	fi
	[ "$cep" = "$from" ] || err epoch "the committed policy belongs to controller epoch ${cep:-none}, not $from"
	[ "${crev:-0}" = "$rev" ] && [ "$chash" = "$hash" ] ||
		err changed "the committed policy changed since it was compared (revision ${crev:-0}, hash $chash)"
	committed_meta "$chash" "$crev" "$epoch" "$(jget "$C" transaction_id)" "$(jget "$C" live_hash)" \
		"$(jget "$C" sets_generation)" "$(jget "$C" rules_hash)" || err state "cannot write committed.json"
	kv state rebased
	kv committed_epoch "$epoch"
}

cmd_status() {
	kv tool_version "$TOOL_VERSION"
	kv journal_state "$(jget "$J" state)"
	kv transaction_id "$(jget "$J" transaction_id)"
	kv deadline_unix "$(jget "$J" deadline_unix)"
	kv candidate_hash "$(jhash candidate_hash)"
	kv reason "$(jget "$J" reason)"
	kv tx_revision "$(jget "$J" revision)"
	kv tx_epoch "$(jget "$J" controller_epoch)"
	kv committed_hash "$(jget "$C" hash)"
	kv committed_revision "$(jget "$C" revision)"
	kv committed_epoch "$(jget "$C" controller_epoch)"
	kv committed_tx "$(jget "$C" transaction_id)"
	kv sets_generation "$(jget "$C" sets_generation)"
	kv rules_hash "$(jget "$C" rules_hash)"
	kv expected_live "$(jget "$C" live_hash)"
	kv live_hash "$(livehash)"
	if systemctl is-enabled drool-firewall.service >/dev/null 2>&1; then kv boot_unit enabled; else kv boot_unit disabled; fi
	kv now "$(now)"
}

case ${1:-} in
apply) shift; cmd_apply "$@" ;;
commit) shift; cmd_commit "$@" ;;
rollback) shift; cmd_rollback "$@" ;;
boot) shift; cmd_boot ;;
sets) shift; cmd_sets "$@" ;;
rebase) shift; cmd_rebase "$@" ;;
status) cmd_status ;;
version) echo "$TOOL_VERSION" ;;
*) err usage "usage: drool-fw apply|commit|rollback|boot|sets|rebase|status|version" ;;
esac
