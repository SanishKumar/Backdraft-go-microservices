CREATE TABLE IF NOT EXISTS orders (
  id CHAR(27) PRIMARY KEY,
  created_at TIMESTAMP WITH TIME ZONE NOT NULL,
  account_id CHAR(27) NOT NULL,
  -- NUMERIC rather than MONEY: MONEY formatting depends on lc_monetary.
  total_price NUMERIC(12, 2) NOT NULL,
  idempotency_key TEXT,
  UNIQUE (account_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS orders_account_recent ON orders (account_id, created_at DESC, id DESC);

-- Line items snapshot the product as it was sold.
CREATE TABLE IF NOT EXISTS order_products (
  order_id CHAR(27) NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
  product_id CHAR(27) NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  price NUMERIC(12, 2) NOT NULL,
  quantity INT NOT NULL CHECK (quantity > 0),
  PRIMARY KEY (order_id, product_id)
);
