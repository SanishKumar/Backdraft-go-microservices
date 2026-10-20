"""Exercise a running lab or Compose stack using only Python's standard library."""

import argparse
import json
import time
import urllib.error
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="http://127.0.0.1:7070")
    parser.add_argument("--gateway", default="http://127.0.0.1:8080/graphql")
    parser.add_argument("--wait", type=float, default=60)
    args = parser.parse_args()

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

    # Pause generated traffic while checking public API data correctness.
    before = request(args.api + "/api/load")
    request(args.api + "/api/load", {"rps": 0}, "PUT")
    try:
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
        request(args.api + "/api/reset", {})
        print("Smoke check passed: UI, five agents, checkout, idempotency, order history, live policy.")
    finally:
        request(args.api + "/api/load", before, "PUT")


if __name__ == "__main__":
    main()
