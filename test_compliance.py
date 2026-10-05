#!/usr/bin/env python3
"""
Cross-language compliance test for QTI Neon.

Uses Java as the source of truth to check that a Java host and Java client can communicate
with a respective client and host from all other implementations. The test verifies that both
sides of the library can each register with neon-relay.quietterminal.co.uk:7777, connect, and
exchange game packets cleanly.

Steps
-----
1. Build + install the Java library to the local Maven cache  (mvn install)
2. Compile small Java host/client runner programs against the installed JAR
3. Create a Python venv and install the Python library
4. Build the TypeScript library  (npm install && npm run build)
5. Build the Go library  (go mod tidy && go build), via a throwaway module with
   a replace directive pointing at the local go/ package
6. Test A  — Java host + Python client: client sends a packet, host logs receipt
7. Test B  — Python host + Java client: client sends a packet, host logs receipt
8. Test C  — Java host + TypeScript client: client sends a packet, host logs receipt
9. Test D  — TypeScript host + Java client: client sends a packet, host logs receipt
10. Test E — Java host + Godot client: client sends a packet, host logs receipt
11. Test F — Godot host + Java client: client sends a packet, host logs receipt
12. Test G — Java host + Go client: client sends a packet, host logs receipt
13. Test H — Go host + Java client: client sends a packet, host logs receipt
14. Cleanup — remove the Maven artifact and delete the venv + work directory
"""

import os
import shutil
import subprocess
import sys
import textwrap
import threading
import time
import venv as _venv

# Constants

RELAY     = os.environ.get("NEON_COMPLIANCE_RELAY", "neon-relay.quietterminal.co.uk:7777")
SESSION_A = 9901   # Java host / Python client
SESSION_B = 9902   # Python host / Java client
SESSION_C = 9903   # Java host / TypeScript client
SESSION_D = 9904   # TypeScript host / Java client
SESSION_E = 9905   # Java host / Godot client
SESSION_F = 9906   # Godot host / Java client
SESSION_G = 9907   # Java host / Go client
SESSION_H = 9908   # Go host / Java client

BASE_DIR    = os.path.dirname(os.path.abspath(__file__))
JAVA_DIR    = os.path.join(BASE_DIR, "java")
PYTHON_DIR  = os.path.join(BASE_DIR, "python")
JS_TS_DIR   = os.path.join(BASE_DIR, "js-ts")
JS_TS_DIST  = os.path.join(JS_TS_DIR, "dist")
GODOT_DIR   = os.path.join(BASE_DIR, "godot")
GO_DIR      = os.path.join(BASE_DIR, "go")
WORK_DIR    = os.path.join(BASE_DIR, ".compliance_work")
VENV_DIR    = os.path.join(BASE_DIR, ".compliance_venv")
VENV_PYTHON = os.path.join(VENV_DIR, "bin", "python")
M2_ARTIFACT = os.path.expanduser("~/.m2/repository/com/quietterminal")
JAR_PATH    = os.path.expanduser(
    "~/.m2/repository/com/quietterminal/qti-neon/1.0.0/qti-neon-1.0.0.jar"
)

_PASS = "\033[32mPASS\033[0m"
_FAIL = "\033[31mFAIL\033[0m"


# Java source templates

_JAVA_HOST = textwrap.dedent("""\
    import com.quietterminal.neon.host.NeonHost;
    import com.quietterminal.neon.core.NeonConfig;

    public class NeonHostRunner {
        public static void main(String[] args) throws Exception {
            int sessionId = Integer.parseInt(args[0]);
            String relay  = args[1];

            NeonConfig cfg  = NeonConfig.defaults();
            NeonHost   host = new NeonHost(sessionId, relay, cfg);

            host.setClientConnectCallback((id, name, sid) -> {
                System.out.println("CLIENT_CONNECTED:" + (id & 0xFF) + ":" + name);
                System.out.flush();
            });
            host.setUnhandledPacketCallback((type, sender, payload) -> {
                System.out.println("PACKET_RECEIVED:" + (type & 0xFF) + ":" + (sender & 0xFF));
                System.out.flush();
            });

            Thread hostThread = Thread.ofVirtual().start(() -> {
                try { host.startAndRun(); }
                catch (Exception e) { System.err.println("Host error: " + e.getMessage()); }
            });

            long deadline = System.currentTimeMillis() + 10_000;
            while (!host.isRunning() && System.currentTimeMillis() < deadline)
                Thread.sleep(50);

            if (!host.isRunning()) {
                System.out.println("HOST_FAILED");
                System.out.flush();
                System.exit(1);
            }

            System.out.println("HOST_READY");
            System.out.flush();

            System.in.read();

            if (host.isRunning()) host.stop();
            hostThread.join(3000);
        }
    }
""")

_JAVA_CLIENT = textwrap.dedent("""\
    import com.quietterminal.neon.client.NeonClient;
    import com.quietterminal.neon.core.NeonConfig;

    public class NeonClientRunner {
        public static void main(String[] args) throws Exception {
            int sessionId = Integer.parseInt(args[0]);
            String relay  = args[1];

            NeonConfig cfg    = NeonConfig.defaults();
            NeonClient client = new NeonClient("java-client", cfg);

            client.setUnhandledPacketCallback((type, sender, payload) -> {
                System.out.println("PACKET_RECEIVED:" + (type & 0xFF) + ":" + (sender & 0xFF));
                System.out.flush();
            });

            boolean ok = client.connect(sessionId, relay);
            if (!ok) {
                System.out.println("CONNECT_FAILED");
                System.out.flush();
                System.exit(1);
            }

            System.out.println("CONNECTED:" + (client.getClientId() & 0xFF));
            System.out.flush();

            Thread runThread = Thread.ofVirtual().start(client::run);

            Thread.sleep(300);
            client.sendPacket(new byte[]{0x42}, (byte) 0x10, (byte) 1);
            System.out.println("PACKET_SENT");
            System.out.flush();

            Thread.sleep(1500);
            if (client.isRunning()) client.stop();
            runThread.join(3000);
        }
    }
""")


# Python script templates

_PY_HOST = textwrap.dedent("""\
    import sys, threading, time
    from qti_neon import NeonHost

    session_id = int(sys.argv[1])
    relay      = sys.argv[2]

    host = NeonHost(session_id=session_id, relay_address=relay)
    host.set_client_connect_callback(
        lambda cid, name, sid: print(f"CLIENT_CONNECTED:{cid}:{name}", flush=True)
    )
    host.set_unhandled_packet_callback(
        lambda ptype, sender, payload: print(f"PACKET_RECEIVED:{ptype}:{sender}", flush=True)
    )

    threading.Thread(target=host.start_and_run, daemon=True).start()

    deadline = time.monotonic() + 10
    while not host.is_running and time.monotonic() < deadline:
        time.sleep(0.05)

    if not host.is_running:
        print("HOST_FAILED", flush=True)
        sys.exit(1)

    print("HOST_READY", flush=True)

    try:
        sys.stdin.read()
    except Exception:
        pass

    if host.is_running:
        host.stop()
""")

_PY_CLIENT = textwrap.dedent("""\
    import sys, threading, time
    from qti_neon import NeonClient

    session_id = int(sys.argv[1])
    relay      = sys.argv[2]

    client = NeonClient("python-client")
    client.set_unhandled_packet_callback(
        lambda ptype, sender, payload: print(f"PACKET_RECEIVED:{ptype}:{sender}", flush=True)
    )

    ok = client.connect(session_id=session_id, relay_address=relay)
    if not ok:
        print("CONNECT_FAILED", flush=True)
        sys.exit(1)

    print(f"CONNECTED:{client.client_id}", flush=True)
    threading.Thread(target=client.run, daemon=True).start()

    time.sleep(0.3)
    client.send_packet(b"\\x42", packet_type=0x10, dest_id=1)
    print("PACKET_SENT", flush=True)

    time.sleep(1.5)
    if client.is_running:
        client.stop()
""")


# TypeScript / Node.js script templates
# These are written as plain .js files that require the compiled dist output.

_TS_HOST = textwrap.dedent("""\
    'use strict';
    const {{ NeonHost }} = require({dist!r});

    const sessionId = parseInt(process.argv[2]);
    const relay     = process.argv[3];

    const host = new NeonHost(sessionId, relay);

    host.setClientConnectCallback((id, name) => {{
        console.log('CLIENT_CONNECTED:' + id + ':' + name);
    }});
    host.setUnhandledPacketCallback((type, sender) => {{
        console.log('PACKET_RECEIVED:' + type + ':' + sender);
    }});

    host.start().then(() => {{
        console.log('HOST_READY');
        process.stdin.resume();
        process.stdin.on('end', () => {{
            host.stop().then(() => process.exit(0)).catch(() => process.exit(1));
        }});
    }}).catch(err => {{
        console.error('Host start failed: ' + err.message);
        console.log('HOST_FAILED');
        process.exit(1);
    }});
""")

_TS_CLIENT = textwrap.dedent("""\
    'use strict';
    const {{ NeonClient }} = require({dist!r});

    const sessionId = parseInt(process.argv[2]);
    const relay     = process.argv[3];

    const client = new NeonClient('ts-client');
    client.setUnhandledPacketCallback((type, sender) => {{
        console.log('PACKET_RECEIVED:' + type + ':' + sender);
    }});

    client.connect(sessionId, relay).then(ok => {{
        if (!ok) {{
            console.log('CONNECT_FAILED');
            process.exit(1);
            return;
        }}
        console.log('CONNECTED:' + client.currentClientId);

        setTimeout(() => {{
            client.sendPacket(Buffer.from([0x42]), 0x10, 1);
            console.log('PACKET_SENT');

            setTimeout(() => {{
                if (client.isRunning) client.stop();
            }}, 1500);
        }}, 300);
    }}).catch(err => {{
        console.error('Connect failed: ' + err.message);
        console.log('CONNECT_FAILED');
        process.exit(1);
    }});
""")


# Go source templates

_GO_HOST = textwrap.dedent("""\
    package main

    import (
        "bufio"
        "fmt"
        "os"
        "strconv"
        "time"

        qtineon "github.com/Quiet-Terminal-Interactive/QTINeon/go"
    )

    func main() {
        sessionID, _ := strconv.Atoi(os.Args[1])
        relay := os.Args[2]

        cfg := qtineon.DefaultConfig()
        host, err := qtineon.NewHost(int32(sessionID), relay, cfg)
        if err != nil {
            fmt.Println("HOST_FAILED")
            os.Exit(1)
        }

        host.SetClientConnectCallback(func(id uint8, name string, sid int32) {
            fmt.Printf("CLIENT_CONNECTED:%d:%s\\n", id, name)
        })
        host.SetUnhandledPacketCallback(func(ptype, sender uint8, payload []byte) {
            fmt.Printf("PACKET_RECEIVED:%d:%d\\n", ptype, sender)
        })

        go func() { _ = host.StartAndRun() }()

        deadline := time.Now().Add(10 * time.Second)
        for !host.IsRunning() && time.Now().Before(deadline) {
            time.Sleep(50 * time.Millisecond)
        }
        if !host.IsRunning() {
            fmt.Println("HOST_FAILED")
            os.Exit(1)
        }

        fmt.Println("HOST_READY")

        bufio.NewReader(os.Stdin).ReadByte()

        if host.IsRunning() {
            _ = host.Stop()
        }
    }
""")

_GO_CLIENT = textwrap.dedent("""\
    package main

    import (
        "fmt"
        "os"
        "strconv"
        "time"

        qtineon "github.com/Quiet-Terminal-Interactive/QTINeon/go"
    )

    func main() {
        sessionID, _ := strconv.Atoi(os.Args[1])
        relay := os.Args[2]

        cfg := qtineon.DefaultConfig()
        client, err := qtineon.NewClient("go-client", cfg)
        if err != nil {
            fmt.Println("CONNECT_FAILED")
            os.Exit(1)
        }

        client.SetUnhandledPacketCallback(func(ptype, sender uint8, payload []byte) {
            fmt.Printf("PACKET_RECEIVED:%d:%d\\n", ptype, sender)
        })

        ok := client.Connect(int32(sessionID), relay)
        if !ok {
            fmt.Println("CONNECT_FAILED")
            os.Exit(1)
        }

        cid, _ := client.ClientID()
        fmt.Printf("CONNECTED:%d\\n", cid)

        go client.Run()

        time.Sleep(300 * time.Millisecond)
        _ = client.SendPacket([]byte{0x42}, 0x10, 1)
        fmt.Println("PACKET_SENT")

        time.Sleep(1500 * time.Millisecond)
        if client.IsRunning() {
            _ = client.Stop()
        }
    }
""")

_GO_MOD = textwrap.dedent("""\
    module compliance

    go 1.22

    require github.com/Quiet-Terminal-Interactive/QTINeon/go v0.0.0-00010101000000-000000000000

    replace github.com/Quiet-Terminal-Interactive/QTINeon/go => {go_dir}
""")


# Helpers

def _section(title: str) -> None:
    print(f"\n{'─' * 62}")
    print(f"  {title}")
    print(f"{'─' * 62}")


def _run(cmd: list, cwd: str | None = None) -> None:
    print(f"  $ {' '.join(str(c) for c in cmd)}")
    subprocess.run(cmd, cwd=cwd, check=True)


def _collect(proc, lines: list, events: dict) -> None:
    for raw in proc.stdout:
        line = raw.rstrip()
        lines.append(line)
        print(f"    {line}")
        for marker, ev in events.items():
            if marker in line:
                ev.set()


def _launch(cmd: list, ready_marker: str, timeout: int = 20,
            use_stdin_pipe: bool = True):
    lines: list[str] = []
    ready = threading.Event()
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stdin=subprocess.PIPE if use_stdin_pipe else None,
        stderr=subprocess.STDOUT,
        text=True,
        bufsize=1,
    )
    threading.Thread(
        target=_collect,
        args=(proc, lines, {ready_marker: ready}),
        daemon=True,
    ).start()
    if not ready.wait(timeout=timeout):
        proc.kill()
        raise RuntimeError(
            f"Timed out after {timeout}s waiting for '{ready_marker}'"
        )
    return proc, lines


def _java_cmd(main_class: str, *args) -> list:
    cp = f"{WORK_DIR}:{JAR_PATH}"
    return ["java", "-cp", cp, main_class, *args]


def _godot_cmd(script: str, *args) -> list:
    return ["godot", "--headless", "--path", GODOT_DIR, "--script", script, "--", *args]


def _go_cmd(binary: str, *args) -> list:
    return [os.path.join(WORK_DIR, "gomod", binary), *args]


# Tests

def _test_java_host_python_client() -> None:
    _section(f"Test A: Java host  ←→  Python client   (session {SESSION_A})")

    print("  Starting Java host…")
    host_proc, host_lines = _launch(
        _java_cmd("NeonHostRunner", str(SESSION_A), RELAY),
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting Python client…")
    client_proc, client_lines = _launch(
        [VENV_PYTHON, os.path.join(WORK_DIR, "py_client.py"), str(SESSION_A), RELAY],
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Java host registered Python client connection")
    else:
        print(f"  {_FAIL}  Java host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Java host received game packet from Python client")
    else:
        raise RuntimeError("Java host did not receive the game packet from Python client")


def _test_python_host_java_client() -> None:
    _section(f"Test B: Python host  ←→  Java client   (session {SESSION_B})")

    print("  Starting Python host…")
    host_proc, host_lines = _launch(
        [VENV_PYTHON, os.path.join(WORK_DIR, "py_host.py"), str(SESSION_B), RELAY],
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting Java client…")
    client_proc, client_lines = _launch(
        _java_cmd("NeonClientRunner", str(SESSION_B), RELAY),
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Python host registered Java client connection")
    else:
        print(f"  {_FAIL}  Python host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Python host received game packet from Java client")
    else:
        raise RuntimeError("Python host did not receive the game packet from Java client")


def _test_java_host_ts_client() -> None:
    _section(f"Test C: Java host  ←→  TypeScript client   (session {SESSION_C})")

    print("  Starting Java host…")
    host_proc, host_lines = _launch(
        _java_cmd("NeonHostRunner", str(SESSION_C), RELAY),
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting TypeScript client…")
    client_proc, client_lines = _launch(
        ["node", os.path.join(WORK_DIR, "ts_client.js"), str(SESSION_C), RELAY],
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Java host registered TypeScript client connection")
    else:
        print(f"  {_FAIL}  Java host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Java host received game packet from TypeScript client")
    else:
        raise RuntimeError("Java host did not receive the game packet from TypeScript client")


def _test_ts_host_java_client() -> None:
    _section(f"Test D: TypeScript host  ←→  Java client   (session {SESSION_D})")

    print("  Starting TypeScript host…")
    host_proc, host_lines = _launch(
        ["node", os.path.join(WORK_DIR, "ts_host.js"), str(SESSION_D), RELAY],
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting Java client…")
    client_proc, client_lines = _launch(
        _java_cmd("NeonClientRunner", str(SESSION_D), RELAY),
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  TypeScript host registered Java client connection")
    else:
        print(f"  {_FAIL}  TypeScript host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  TypeScript host received game packet from Java client")
    else:
        raise RuntimeError("TypeScript host did not receive the game packet from Java client")


def _test_java_host_godot_client() -> None:
    _section(f"Test E: Java host  ←→  Godot client   (session {SESSION_E})")

    print("  Starting Java host…")
    host_proc, host_lines = _launch(
        _java_cmd("NeonHostRunner", str(SESSION_E), RELAY),
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting Godot client…")
    client_proc, client_lines = _launch(
        _godot_cmd("compliance/neon_client_runner.gd", str(SESSION_E), RELAY),
        ready_marker="PACKET_SENT",
        timeout=25,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Java host registered Godot client connection")
    else:
        print(f"  {_FAIL}  Java host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Java host received game packet from Godot client")
    else:
        raise RuntimeError("Java host did not receive the game packet from Godot client")


def _test_godot_host_java_client() -> None:
    _section(f"Test F: Godot host  ←→  Java client   (session {SESSION_F})")

    print("  Starting Godot host…")
    host_proc, host_lines = _launch(
        _godot_cmd("compliance/neon_host_runner.gd", str(SESSION_F), RELAY),
        ready_marker="HOST_READY",
        timeout=25,
    )

    print("  Starting Java client…")
    client_proc, client_lines = _launch(
        _java_cmd("NeonClientRunner", str(SESSION_F), RELAY),
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Godot host registered Java client connection")
    else:
        print(f"  {_FAIL}  Godot host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Godot host received game packet from Java client")
    else:
        raise RuntimeError("Godot host did not receive the game packet from Java client")


def _test_java_host_go_client() -> None:
    _section(f"Test G: Java host  ←→  Go client   (session {SESSION_G})")

    print("  Starting Java host…")
    host_proc, host_lines = _launch(
        _java_cmd("NeonHostRunner", str(SESSION_G), RELAY),
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting Go client…")
    client_proc, client_lines = _launch(
        _go_cmd("go_client", str(SESSION_G), RELAY),
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Java host registered Go client connection")
    else:
        print(f"  {_FAIL}  Java host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Java host received game packet from Go client")
    else:
        raise RuntimeError("Java host did not receive the game packet from Go client")


def _test_go_host_java_client() -> None:
    _section(f"Test H: Go host  ←→  Java client   (session {SESSION_H})")

    print("  Starting Go host…")
    host_proc, host_lines = _launch(
        _go_cmd("go_host", str(SESSION_H), RELAY),
        ready_marker="HOST_READY",
        timeout=20,
    )

    print("  Starting Java client…")
    client_proc, client_lines = _launch(
        _java_cmd("NeonClientRunner", str(SESSION_H), RELAY),
        ready_marker="PACKET_SENT",
        timeout=20,
        use_stdin_pipe=False,
    )

    time.sleep(2.0)
    client_proc.wait(timeout=5)

    ok_conn   = any("CLIENT_CONNECTED" in l for l in host_lines)
    ok_packet = any("PACKET_RECEIVED"  in l for l in host_lines)

    try:
        host_proc.stdin.close()
        host_proc.wait(timeout=8)
    except Exception:
        host_proc.kill()

    if ok_conn:
        print(f"  {_PASS}  Go host registered Java client connection")
    else:
        print(f"  {_FAIL}  Go host never saw CLIENT_CONNECTED")

    if ok_packet:
        print(f"  {_PASS}  Go host received game packet from Java client")
    else:
        raise RuntimeError("Go host did not receive the game packet from Java client")


# Main

def main() -> int:
    failures: list[str] = []

    _section("Step 1: Build and install Java library")
    _run(["mvn", "-f", os.path.join(JAVA_DIR, "pom.xml"), "install", "-DskipTests"])

    _section("Step 2: Compile Java host/client runners")
    os.makedirs(WORK_DIR, exist_ok=True)

    host_java   = os.path.join(WORK_DIR, "NeonHostRunner.java")
    client_java = os.path.join(WORK_DIR, "NeonClientRunner.java")
    with open(host_java,   "w") as f: f.write(_JAVA_HOST)
    with open(client_java, "w") as f: f.write(_JAVA_CLIENT)

    _run(["javac", "-cp", JAR_PATH, "-d", WORK_DIR, host_java, client_java])

    _section("Step 3: Create Python venv and install library")
    _venv.create(VENV_DIR, with_pip=True, clear=True)
    _run([VENV_PYTHON, "-m", "pip", "install", "--quiet", PYTHON_DIR])

    with open(os.path.join(WORK_DIR, "py_host.py"),   "w") as f: f.write(_PY_HOST)
    with open(os.path.join(WORK_DIR, "py_client.py"), "w") as f: f.write(_PY_CLIENT)

    _section("Step 4: Build TypeScript library")
    _run(["npm", "install", "--prefer-offline"], cwd=JS_TS_DIR)
    _run(["npm", "run", "build"],                cwd=JS_TS_DIR)

    dist_index = os.path.join(JS_TS_DIST, "index.js")
    with open(os.path.join(WORK_DIR, "ts_host.js"),   "w") as f:
        f.write(_TS_HOST.format(dist=dist_index))
    with open(os.path.join(WORK_DIR, "ts_client.js"), "w") as f:
        f.write(_TS_CLIENT.format(dist=dist_index))

    _section("Step 5: Build Go library")
    gomod_dir = os.path.join(WORK_DIR, "gomod")
    os.makedirs(gomod_dir, exist_ok=True)

    with open(os.path.join(gomod_dir, "go.mod"), "w") as f:
        f.write(_GO_MOD.format(go_dir=GO_DIR))
    with open(os.path.join(gomod_dir, "go_host.go"), "w") as f:
        f.write(_GO_HOST)
    with open(os.path.join(gomod_dir, "go_client.go"), "w") as f:
        f.write(_GO_CLIENT)

    _run(["go", "mod", "tidy"], cwd=gomod_dir)
    _run(["go", "build", "-o", "go_host", "go_host.go"], cwd=gomod_dir)
    _run(["go", "build", "-o", "go_client", "go_client.go"], cwd=gomod_dir)

    try:
        _test_java_host_python_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test A failed: {e}")
        failures.append(f"Test A: {e}")

    try:
        _test_python_host_java_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test B failed: {e}")
        failures.append(f"Test B: {e}")

    try:
        _test_java_host_ts_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test C failed: {e}")
        failures.append(f"Test C: {e}")

    try:
        _test_ts_host_java_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test D failed: {e}")
        failures.append(f"Test D: {e}")

    try:
        _test_java_host_godot_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test E failed: {e}")
        failures.append(f"Test E: {e}")

    try:
        _test_godot_host_java_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test F failed: {e}")
        failures.append(f"Test F: {e}")

    try:
        _test_java_host_go_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test G failed: {e}")
        failures.append(f"Test G: {e}")

    try:
        _test_go_host_java_client()
    except Exception as e:
        print(f"\n  {_FAIL}  Test H failed: {e}")
        failures.append(f"Test H: {e}")

    _section("Step 6: Cleanup")

    if os.path.exists(M2_ARTIFACT):
        shutil.rmtree(M2_ARTIFACT)
        print(f"  Removed Maven artifact:  {M2_ARTIFACT}")

    if os.path.exists(VENV_DIR):
        shutil.rmtree(VENV_DIR)
        print(f"  Removed Python venv:     {VENV_DIR}")

    if os.path.exists(WORK_DIR):
        shutil.rmtree(WORK_DIR)
        print(f"  Removed work directory:  {WORK_DIR}")

    _section("Results")
    if failures:
        for msg in failures:
            print(f"  {_FAIL}  {msg}")
        print()
        return 1

    print(f"  {_PASS}  All tests passed")
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
