#!/usr/bin/env python3
"""A dependency-free atto JSON-lines client. See README.md in this directory."""
import argparse
import json
import os
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("prompt", nargs="?", default="Say hello briefly")
    parser.add_argument("--atto", default="atto", help="path to the atto executable")
    parser.add_argument("--model", help="configured provider/model ID")
    parser.add_argument("--in-process", action="store_true")
    args = parser.parse_args()
    command = [args.atto, "app-server", "--listen", "stdio://"]
    if args.in_process:
        command.append("--in-process")
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               text=True, encoding="utf-8", bufsize=1,
                               start_new_session=(os.name != "nt"))
    seq, thread, finished = 0, None, False
    item_types = {}

    def send(method, params, request_id=None):
        message = {"method": method, "params": params}
        if request_id is not None:
            message["id"] = request_id
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()

    def event(message):
        nonlocal finished
        method, params = message.get("method"), message.get("params", {})
        if method == "events/reset":
            raise RuntimeError("Event gap: read a fresh thread snapshot before continuing")
        if params.get("threadId") != thread:
            return
        if method in ("item/started", "item/updated", "item/completed"):
            item = params["item"]
            item_types[item["id"]] = item["type"]
        elif method == "item/delta" and item_types.get(params["itemId"]) == "agentMessage":
            print(params["delta"], end="", flush=True)
        elif method == "prompt/open":
            prompt = params["prompt"]
            print("\n" + prompt["title"], file=sys.stderr)
            answer = {"threadId": thread, "id": prompt["id"]}
            try:
                if prompt["kind"] == "select":
                    for index, option in enumerate(prompt.get("options", [])):
                        print(f"  {index + 1}. {option['label']}", file=sys.stderr)
                    print("Choose number (empty cancels): ", end="", file=sys.stderr, flush=True)
                    text = input()
                    if text:
                        index = int(text) - 1
                        if not 0 <= index < len(prompt.get("options", [])):
                            raise ValueError("choice out of range")
                        answer["index"] = index
                    else:
                        answer["cancel"] = True
                else:
                    print("Answer: ", end="", file=sys.stderr, flush=True)
                    answer["text"] = input()
            except (EOFError, ValueError):
                answer["cancel"] = True
            # Prompts are server objects, not JSON-RPC requests with an id.
            send("prompt/answer", answer)
        elif method == "turn/completed":
            finished = True
            if params.get("error"):
                raise RuntimeError(params["error"])

    def read():
        line = process.stdout.readline()
        if not line:
            raise RuntimeError("atto disconnected")
        return json.loads(line)

    def call(method, params):
        nonlocal seq
        seq += 1
        request_id = seq
        send(method, params, request_id)
        while True:
            message = read()
            if message.get("id") == request_id:
                if "error" in message:
                    raise RuntimeError(message["error"])
                return message["result"]
            event(message)

    try:
        call("initialize", {"protocolVersions": [2], "clientInfo": {"name": "python-example", "version": "1"},
                            "capabilities": {"interactive": True}})
        send("initialized", {})
        params = {"model": args.model} if args.model else {}
        thread = call("thread/start", params)["threadId"]
        call("turn/start", {"threadId": thread, "input": args.prompt})
        while not finished:
            event(read())
        print()
    except KeyboardInterrupt:
        if thread:
            send("turn/interrupt", {"threadId": thread, "mode": "cancel"})
    finally:
        # EOF detaches from daemon workers; it is not thread/close.
        process.stdin.close()
        process.wait()
        process.stdout.close()


if __name__ == "__main__":
    main()
