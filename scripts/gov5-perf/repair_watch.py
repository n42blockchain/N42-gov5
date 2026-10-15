#!/usr/bin/env python3
"""Fixed f1015a repair monitor; resource waits never retry a started fleet."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time

DEFAULT_ARTIFACTS = Path("/data/blockchain/gov5-work/validation/20261015-e1-fcdb22d2/root-rules-fb1671a4")
RESOURCE_ERRORS = ("hardware blocked: ", "hardware contention after claim: ")


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate result field")
        result[key] = value
    return result


def resource_wait_only(code, log_path, attempt_path):
    """7774408d creates this attempt before spawning any fleet child."""
    if code != 1 or os.path.lexists(attempt_path):
        return False
    try:
        with open(log_path, "rb") as source:
            raw = source.read(4097)
        if len(raw) > 4096:
            return False
        result = json.loads(raw, object_pairs_hook=unique_fields)
    except (OSError, ValueError, UnicodeError):
        return False
    if not isinstance(result, dict) or set(result) != {"error", "kind"} or result["kind"] != "functional-check-not-tps":
        return False
    error = result["error"]
    return isinstance(error, str) and (any(error.startswith(prefix) and len(error) > len(prefix) for prefix in RESOURCE_ERRORS)
                                     or error == "hardware contention: owned BOX claim changed")


def functional_when_available(run_once, attempt_path, waiting, wait=time.sleep):
    number = 1
    while True:
        name = f"functional-run-{number:03d}"
        code, log_path = run_once(name)
        if code == 0:
            return
        if not resource_wait_only(code, log_path, attempt_path):
            raise RuntimeError(name + " failed; retain existing attempt/data for review")
        waiting(name, log_path)
        wait(60)
        number += 1


class RepairWatch:
    def __init__(self, artifacts):
        self.artifacts = artifacts
        self.proof = json.loads((artifacts / "provenance.json").read_text())
        self.source = Path(self.proof["node_source_tree"])
        self.expected = self.proof["node_source_commit"]
        self.runner_directory = Path(self.proof["runner_path"]).parent
        runner_hashes = self.verify_runner()
        sys.path.insert(0, str(self.runner_directory))
        import fleet
        self.fleet = fleet
        prior_path = artifacts / "status.json"
        prior = json.loads(prior_path.read_text()) if prior_path.exists() else {}
        if prior.get("stages"):
            raise RuntimeError("existing stages require explicit recovery review")
        self.state = dict(prior, phase="waiting", stages=[], node_source_commit=self.expected,
                          main_cherry_pick=self.proof["main_cherry_pick"], runner_commit=self.proof["runner_commit"],
                          runtime_root=self.proof["runtime_root"], attempt=self.proof["attempt"],
                          watcher_source_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), runner_sha256=runner_hashes)
        self.state.pop("error", None)
        self.active = None

    def verify_runner(self):
        # The retry proof depends on this exact ordering: create the attempt
        # before every fleet spawn. Verify both modules before import/run.
        if self.proof["runner_commit"] != "7774408d6cf17f74fe49aaf25badb7626204365b":
            raise RuntimeError("unexpected pinned runner commit")
        hashes = {}
        for name in ("fleet.py", "preflight.py"):
            committed = subprocess.check_output(["git", "show", self.proof["runner_commit"] + ":scripts/gov5-perf/" + name], cwd=self.source)
            actual = (self.runner_directory / name).read_bytes()
            if actual != committed:
                raise RuntimeError("pinned runner module changed: " + name)
            hashes[name] = hashlib.sha256(actual).hexdigest()
        return hashes

    def say(self, message):
        print(time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), message, flush=True)

    def save(self):
        temporary = self.artifacts / "status.tmp"
        with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600), "w") as output:
            json.dump(self.state, output, indent=2)
        os.replace(temporary, self.artifacts / "status.json")

    def clean_source(self):
        head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.source, text=True).strip()
        dirty = subprocess.check_output(["git", "status", "--porcelain"], cwd=self.source, text=True).strip()
        if head != self.expected or dirty:
            raise RuntimeError("fixed repair source changed")

    def command(self, name, arguments, timeout, allow_failure=False):
        log_path = self.artifacts / (name + ".log")
        entry = {"name": name, "command": arguments, "started_unix": time.time()}
        self.state["stages"].append(entry)
        self.save()
        self.say(name + " starting")
        with os.fdopen(os.open(log_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as log:
            self.active = subprocess.Popen(arguments, cwd=self.source, env=dict(os.environ, GOMAXPROCS="2", GOFLAGS="-p=1"),
                                           stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            try:
                code = self.active.wait(timeout=timeout)
                entry["exit_code"] = code
            except subprocess.TimeoutExpired:
                entry["outcome"] = "timeout"
                raise
            finally:
                if self.active.poll() is None:
                    os.killpg(self.active.pid, signal.SIGTERM)
                    try:
                        self.active.wait(timeout=90 if name.startswith("functional-run-") else 10)
                    except subprocess.TimeoutExpired:
                        os.killpg(self.active.pid, signal.SIGKILL)
                        self.active.wait(timeout=10)
                self.active = None
                entry["finished_unix"] = time.time()
                self.save()
        self.say(name + " exit=" + str(code))
        if code and not allow_failure:
            raise RuntimeError(name + " failed; existing data retained")
        return code, log_path

    def run(self):
        self.save()
        self.say("passive repair monitor started; resource-only prelaunch retry enabled")
        try:
            self.clean_source()
            for name, info in self.proof["reused_binaries"].items():
                if name != "n42" and self.fleet.binary(info["path"]) != info:
                    raise RuntimeError("unchanged baseline binary SHA differs")
            while True:
                try:
                    self.fleet.quiet_checks()
                    context = self.fleet.claim()
                    resources = context.__enter__()
                    break
                except (OSError, ValueError) as error:
                    self.state["last_wait_reason"] = str(error)
                    self.save()
                    self.say("waiting: " + str(error))
                    time.sleep(30)
            try:
                self.state.update(phase="validation-build", resources=resources)
                self.save()
                self.clean_source()
                deadline = time.monotonic() + 1200
                self.command("race-root-rules", ["go", "test", "-race", "-p", "1", "./internal", "./internal/node", "-run",
                             "^TestConfigureBlockCommitmentRules$|^TestPerformanceValidatorNodeRunsRemoteExecutionWithoutLocalChain$", "-count=1"],
                             max(0.01, deadline - time.monotonic()))
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise subprocess.TimeoutExpired("repair build budget", 1200)
                self.command("build-n42", ["make", "-o", "version-build", "n42", "GIT_TAG=perf-functional-root-rules-fb1671a4"], remaining)
                self.clean_source()
                target = self.artifacts / "bin/n42"
                with open(self.source / "build/bin/n42", "rb") as source, os.fdopen(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o755), "wb") as output:
                    shutil.copyfileobj(source, output)
                os.chmod(target, 0o755)
                self.state["binaries"] = dict(self.proof["reused_binaries"], n42=self.fleet.binary(str(target)))
                self.save()
            finally:
                context.__exit__(*sys.exc_info())
            self.state["phase"] = "waiting-functional"
            self.save()
            self.say("build claim released; functional attempts independently re-check BOX")
            binaries = self.state["binaries"]
            root = Path(self.proof["runtime_root"])
            arguments = [sys.executable, self.proof["runner_path"], str(root / "run.json"), "--run", "--attempt", self.proof["attempt"],
                         "--duration-seconds", "30", "--node-binary", binaries["n42"]["path"], "--owner-binary", binaries["n42-execution-service"]["path"],
                         "--smoke-binary", binaries["gov5-perf-provision"]["path"]]
            def waiting(name, path):
                self.state.update(phase="waiting-functional", last_wait_reason=name + " resource refusal before attempt creation", last_resource_log=str(path))
                self.save()
                self.say(self.state["last_wait_reason"])
            def run_once(name):
                if self.verify_runner() != self.state["runner_sha256"]:
                    raise RuntimeError("pinned runner digest changed")
                result = self.command(name, arguments, 600, allow_failure=True)
                self.verify_runner()
                return result
            functional_when_available(run_once, root / "attempts" / self.proof["attempt"], waiting)
            self.state["phase"] = "functional-passed"
            self.save()
            self.say("same-root functional passed; no TPS claim")
        except BaseException as error:
            self.state.update(phase="stopped-failed", error=str(error))
            self.save()
            self.say("stopped: " + str(error))
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, default=DEFAULT_ARTIFACTS)
    arguments = parser.parse_args()
    def stopped(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stopped)
    signal.signal(signal.SIGINT, stopped)
    RepairWatch(arguments.artifacts).run()


if __name__ == "__main__":
    main()
