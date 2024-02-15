import subprocess
import threading
from pathlib import Path

import bottle
import httpx
import pytest
import stamina


@stamina.retry(on=httpx.HTTPError)
def wait_for_server(url: str) -> None:
    resp = httpx.get(url)
    resp.raise_for_status()


#### Run the proxy as a subprocess


@pytest.fixture(scope="session")
def go_server():
    process = None
    try:
        subprocess.run(["go", "build", "-o", "goproxy", "cmd/main.go"], check=True)
        Path("./db.sqlite").unlink(missing_ok=True)
        process = subprocess.Popen(["./goproxy"])
        wait_for_server("http://127.0.0.1:5000/healthcheck")
        yield process
    finally:
        if process:
            process.terminate()
            process.wait()


#### A target server implementation that we control

app = bottle.Bottle()
counter = 0

@app.route("/count")  # type: ignore
def count():
    global counter
    counter += 1
    return str(counter)


@app.route("/healthcheck")  # type: ignore
def healthcheck():
    return "OK"



def run_server():
    bottle.run(app=app, host="localhost", port=8080)


@pytest.fixture(scope="session")
def bottle_server():
    server_thread = threading.Thread(target=run_server)
    server_thread.daemon = True  # Allows the thread to be automatically killed when the main process finishes
    server_thread.start()
    wait_for_server("http://localhost:8080/healthcheck")
    yield


#### Create a http client with proxy configured



@pytest.fixture(scope="function")
def direct(bottle_server):
    global counter
    counter = 0
    with httpx.Client(
        base_url="http://localhost:8080"
    ) as client:
        yield client


@pytest.fixture(scope="function")
def proxy(bottle_server, go_server):
    global counter
    counter = 0
    with httpx.Client(
        base_url="http://localhost:8080", proxy="http://127.0.0.1:5000"
    ) as client:
        yield client




#### Test if the proxy works
def test_count(direct):
    resp = direct.get("/count")
    assert resp.status_code == 200
    assert resp.text == "1"
    resp = direct.get("/count")
    assert resp.status_code == 200
    assert resp.text == "2"

def test_cached_count(proxy):
    resp = proxy.get("/count")
    assert resp.status_code == 200, resp.text
    assert resp.text == "1"
    resp = proxy.get("/count")
    assert resp.status_code == 200, resp.text
    assert resp.text == "1"
