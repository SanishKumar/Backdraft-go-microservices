"""Exercise a running lab or Compose stack using only Python's standard library."""

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
import uuid


def wait_for_idle(request, api, service_names, timeout):
    """Require fresh, quiet telemetry before sending non-idempotent writes."""
    deadline = time.monotonic() + timeout
    last_tick = None
    idle_samples = 0
    last_error = "waiting for live telemetry"
    while time.monotonic() < deadline:
        try:
            state = request(api + "/api/state")
            live = request(api + "/api/live")
            connected = {s["name"] for s in state["services"] if s["instances"] > 0}
            missing = service_names - (connected & live["services"].keys())
            queued = sum(s["queued"] for s in live["services"].values())
            active = sum(s["inFlight"] for s in live["services"].values())
            attempts = sum(e["attempts"] for e in live["edges"].values())
            faults = [name for name, enabled in state["faults"].items() if enabled]
            if missing or faults or queued or active or attempts or live["entry"]["offered"]:
                idle_samples = 0
                last_error = f"{queued} queued, {active} in flight, {attempts:g} attempts/s"
                if missing:
                    last_error += f"; missing agents: {', '.join(sorted(missing))}"
                if faults:
                    last_error += f"; active faults: {', '.join(sorted(faults))}"
            elif live["t"] != last_tick:
                idle_samples += 1
                last_error = f"waiting for stable idle telemetry ({idle_samples}/3 samples)"
                if idle_samples == 3:
                    return
            last_tick = live["t"]
        except (urllib.error.URLError, TimeoutError, OSError) as error:
            idle_samples = 0
            last_error = str(error)
        time.sleep(1)
    raise RuntimeError(f"lab did not drain within {timeout:g}s: {last_error}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="http://127.0.0.1:7070")
    parser.add_argument("--gateway", default="http://127.0.0.1:8080/graphql")
    parser.add_argument("--wait", type=float, default=60,
                        help="seconds allowed for startup and each recovery phase (default: 60)")
    args = parser.parse_args()
    if args.wait <= 0:
        parser.error("--wait must be positive")

    def request(url, body=None, method=None, headers=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(
            url, data=data, method=method,
            headers={"Content-Type": "application/json", **(headers or {})},
        )
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.load(response)

    deadline = time.monotonic() + args.wait
    last_error = "services not connected"
    while time.monotonic() < deadline:
        try:
            state = request(args.api + "/api/state")
            if state["loadReady"] and len(state["services"]) == 5 and all(
                service["instances"] > 0 for service in state["services"]
            ):
                break
        except (urllib.error.URLError, TimeoutError, OSError) as error:
            last_error = str(error)
        time.sleep(1)
    else:
        raise RuntimeError(f"stack did not become ready: {last_error}")

    with urllib.request.urlopen(args.api + "/", timeout=10) as response:
        if b'<div id="root"' not in response.read():
            raise RuntimeError("production UI was not served")

    def gql(query, variables=None, key=None):
        result = request(
            args.gateway, {"query": query, "variables": variables or {}},
            headers={"Idempotency-Key": key} if key else None,
        )
        if result.get("errors"):
            raise RuntimeError(f"GraphQL errors: {result['errors']}")
        return result["data"]

    # An earlier experiment can leave expired work queued long after its fault
    # ends. Pausing arrivals alone does not make the reference policy healthy.
    before = request(args.api + "/api/load")
    passed = False
    try:
        request(args.api + "/api/load", {**before, "rps": 0}, "PUT")
        print("Preparing the lab: pausing traffic, clearing experiments, and draining expired work.", flush=True)
        request(args.api + "/api/reset", method="POST")
        request(args.api + "/api/mitigations", {
            "retryBudgets": True, "singleLayerRetries": True,
            "propagateDeadlines": True, "shedExpired": True, "boundedQueues": True,
        })
        service_names = {s["name"] for s in state["services"]}
        wait_for_idle(request, args.api, service_names, args.wait)
        # Test checkout under the reference policy, after recovery has finished.
        request(args.api + "/api/reset", method="POST")
        wait_for_idle(request, args.api, service_names, args.wait)

        account = gql('mutation { createAccount(account: {name: "Smoke check"}) { id } }')["createAccount"]
        product = gql('mutation { createProduct(product: {name: "Smoke lamp", description: "CI", price: 12.34}) { id } }')["createProduct"]
        query = """mutation($order: OrderInput!) {
          createOrder(order: $order) { id totalPrice products { id quantity price } }
        }"""
        variables = {"order": {"accountId": account["id"], "products": [{"id": product["id"], "quantity": 2}]}}
        key = "smoke-" + uuid.uuid4().hex
        first = gql(query, variables, key)["createOrder"]
        second = gql(query, variables, key)["createOrder"]
        if first != second or first["totalPrice"] != 24.68 or first["products"][0]["quantity"] != 2:
            raise RuntimeError(f"order creation or idempotency failed: {first}, {second}")
        history = gql("query($id: String!) { accounts(id: $id) { orders { id totalPrice } } }", {"id": account["id"]})
        if first["id"] not in [order["id"] for order in history["accounts"][0]["orders"]]:
            raise RuntimeError("created order is missing from history")

        state = request(args.api + "/api/mitigations", {"retryBudgets": True})
        if not state["mitigations"]["retryBudgets"]:
            raise RuntimeError("retry budget policy did not apply")
        request(args.api + "/api/reset", method="POST")
        passed = True
    finally:
        if passed:
            request(args.api + "/api/load", before, "PUT")
        else:
            print("Smoke check failed; traffic has not been resumed. Inspect the error before restarting load.", file=sys.stderr)
    print("Smoke check passed: UI, five agents, checkout, idempotency, order history, live policy.")


if __name__ == "__main__":
    main()
