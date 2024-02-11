import subprocess
import threading

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
    try:
        subprocess.run(["go", "build", "-o", "goproxy", "./..."], check=True)
        process = subprocess.Popen(["./goproxy"])
        wait_for_server("http://localhost:8000/healthcheck")
        yield process
    finally:
        process.terminate()
        process.wait()


#### A target server implementation that we control

app = bottle.Bottle()


@app.route("/hello")  # type: ignore
def hello():
    return "Hello World!"


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
def hcli(bottle_server, go_server):
    with httpx.Client(
        base_url="http://localhost:8080", proxy="http://localhost:8000"
    ) as client:
        yield client


#### Test if the proxy works


def test_hello(hcli):
    resp = hcli.get("/hello")
    assert resp.status_code == 200
    assert resp.text == "Hello World!"
