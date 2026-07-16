import type { AppFlows, DataModel } from './atlas'

// dataModelFixture is a realistic ~12-entity model spanning four domains, used by
// the mapping tests and available for local dev.
export const dataModelFixture: DataModel = {
  entities: [
    {
      id: 'user',
      name: 'User',
      domain: 'identity',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'email', type: 'text', pk: false },
        { name: 'name', type: 'text', pk: false },
        { name: 'created_at', type: 'timestamptz', pk: false },
      ],
    },
    {
      id: 'session',
      name: 'Session',
      domain: 'identity',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'user_id', type: 'uuid', pk: false },
        { name: 'expires_at', type: 'timestamptz', pk: false },
      ],
    },
    {
      id: 'api-key',
      name: 'ApiKey',
      domain: 'identity',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'user_id', type: 'uuid', pk: false },
        { name: 'hash', type: 'text', pk: false },
        { name: 'revoked', type: 'bool', pk: false },
      ],
    },
    {
      id: 'category',
      name: 'Category',
      domain: 'catalog',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'slug', type: 'text', pk: false },
        { name: 'name', type: 'text', pk: false },
      ],
    },
    {
      id: 'product',
      name: 'Product',
      domain: 'catalog',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'sku', type: 'text', pk: false },
        { name: 'title', type: 'text', pk: false },
        { name: 'price_cents', type: 'int', pk: false },
      ],
    },
    {
      id: 'inventory-item',
      name: 'InventoryItem',
      domain: 'catalog',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'product_id', type: 'uuid', pk: false },
        { name: 'on_hand', type: 'int', pk: false },
        { name: 'reserved', type: 'int', pk: false },
      ],
    },
    {
      id: 'cart',
      name: 'Cart',
      domain: 'orders',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'user_id', type: 'uuid', pk: false },
        { name: 'status', type: 'text', pk: false },
      ],
    },
    {
      id: 'cart-item',
      name: 'CartItem',
      domain: 'orders',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'cart_id', type: 'uuid', pk: false },
        { name: 'product_id', type: 'uuid', pk: false },
        { name: 'qty', type: 'int', pk: false },
      ],
    },
    {
      id: 'order',
      name: 'Order',
      domain: 'orders',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'user_id', type: 'uuid', pk: false },
        { name: 'total_cents', type: 'int', pk: false },
        { name: 'placed_at', type: 'timestamptz', pk: false },
      ],
    },
    {
      id: 'order-line',
      name: 'OrderLine',
      domain: 'orders',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'order_id', type: 'uuid', pk: false },
        { name: 'product_id', type: 'uuid', pk: false },
        { name: 'qty', type: 'int', pk: false },
      ],
    },
    {
      id: 'invoice',
      name: 'Invoice',
      domain: 'billing',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'order_id', type: 'uuid', pk: false },
        { name: 'amount_cents', type: 'int', pk: false },
        { name: 'status', type: 'text', pk: false },
      ],
    },
    {
      id: 'payment',
      name: 'Payment',
      domain: 'billing',
      fields: [
        { name: 'id', type: 'uuid', pk: true },
        { name: 'invoice_id', type: 'uuid', pk: false },
        { name: 'provider_ref', type: 'text', pk: false },
        { name: 'captured_at', type: 'timestamptz', pk: false },
      ],
    },
  ],
  relationships: [
    {
      id: 'user-sessions',
      from: 'user',
      to: 'session',
      cardinality: '1:N',
      label: 'has',
    },
    {
      id: 'user-api-keys',
      from: 'user',
      to: 'api-key',
      cardinality: '1:N',
      label: 'issues',
    },
    {
      id: 'user-carts',
      from: 'user',
      to: 'cart',
      cardinality: '1:N',
      label: 'owns',
    },
    {
      id: 'user-orders',
      from: 'user',
      to: 'order',
      cardinality: '1:N',
      label: 'places',
    },
    {
      id: 'category-products',
      from: 'category',
      to: 'product',
      cardinality: 'N:M',
      label: 'categorises',
    },
    {
      id: 'product-inventory',
      from: 'product',
      to: 'inventory-item',
      cardinality: '1:1',
      label: 'stocked as',
    },
    {
      id: 'cart-items',
      from: 'cart',
      to: 'cart-item',
      cardinality: '1:N',
      label: 'contains',
    },
    {
      id: 'product-cart-items',
      from: 'product',
      to: 'cart-item',
      cardinality: '1:N',
      label: 'referenced by',
    },
    {
      id: 'cart-order',
      from: 'cart',
      to: 'order',
      cardinality: '1:1',
      label: 'checks out to',
    },
    {
      id: 'order-lines',
      from: 'order',
      to: 'order-line',
      cardinality: '1:N',
      label: 'itemises',
    },
    {
      id: 'product-order-lines',
      from: 'product',
      to: 'order-line',
      cardinality: '1:N',
      label: 'sold in',
    },
    {
      id: 'order-invoice',
      from: 'order',
      to: 'invoice',
      cardinality: '1:1',
      label: 'billed by',
    },
    {
      id: 'invoice-payments',
      from: 'invoice',
      to: 'payment',
      cardinality: '1:N',
      label: 'settled by',
    },
  ],
}

// appFlowsFixture is a set of four runtime flows exercising every step kind, used
// by the mapping tests and available for local dev.
export const appFlowsFixture: AppFlows = {
  flows: [
    {
      id: 'checkout',
      name: 'Checkout',
      summary: 'A shopper turns a cart into a paid order.',
      steps: [
        { id: 'view-cart', name: 'View cart', kind: 'ui' },
        { id: 'submit-order', name: 'POST /orders', kind: 'http' },
        { id: 'validate-cart', name: 'Validate cart', kind: 'service' },
        { id: 'reserve-inventory', name: 'Reserve inventory', kind: 'db' },
        { id: 'charge-payment', name: 'Charge payment', kind: 'external' },
        { id: 'persist-order', name: 'Persist order', kind: 'db' },
        {
          id: 'enqueue-fulfillment',
          name: 'Enqueue fulfillment',
          kind: 'queue',
        },
        { id: 'show-confirmation', name: 'Show confirmation', kind: 'ui' },
      ],
      edges: [
        { from: 'view-cart', to: 'submit-order', label: 'checkout' },
        { from: 'submit-order', to: 'validate-cart', label: '' },
        { from: 'validate-cart', to: 'reserve-inventory', label: 'ok' },
        { from: 'validate-cart', to: 'show-confirmation', label: 'invalid' },
        { from: 'reserve-inventory', to: 'charge-payment', label: 'reserved' },
        { from: 'charge-payment', to: 'persist-order', label: 'captured' },
        { from: 'persist-order', to: 'enqueue-fulfillment', label: '' },
        {
          from: 'enqueue-fulfillment',
          to: 'show-confirmation',
          label: 'queued',
        },
      ],
    },
    {
      id: 'signup',
      name: 'Sign up',
      summary: 'A visitor creates an account and lands on the dashboard.',
      steps: [
        { id: 'signup-form', name: 'Sign-up form', kind: 'ui' },
        { id: 'create-user', name: 'POST /users', kind: 'http' },
        { id: 'hash-password', name: 'Hash password', kind: 'service' },
        { id: 'store-user', name: 'Store user', kind: 'db' },
        { id: 'send-welcome', name: 'Send welcome email', kind: 'external' },
        { id: 'redirect-dashboard', name: 'Redirect to dashboard', kind: 'ui' },
      ],
      edges: [
        { from: 'signup-form', to: 'create-user', label: 'submit' },
        { from: 'create-user', to: 'hash-password', label: '' },
        { from: 'hash-password', to: 'store-user', label: '' },
        { from: 'store-user', to: 'send-welcome', label: '' },
        { from: 'store-user', to: 'redirect-dashboard', label: 'session' },
      ],
    },
    {
      id: 'fulfillment',
      name: 'Fulfillment',
      summary: 'A background worker ships a placed order.',
      steps: [
        { id: 'fulfillment-job', name: 'Fulfillment job', kind: 'job' },
        { id: 'pick-items', name: 'Pick items', kind: 'service' },
        { id: 'pack-shipment', name: 'Pack shipment', kind: 'service' },
        { id: 'book-carrier', name: 'Book carrier', kind: 'external' },
        { id: 'save-tracking', name: 'Save tracking', kind: 'db' },
        { id: 'notify-customer', name: 'Notify customer', kind: 'queue' },
      ],
      edges: [
        { from: 'fulfillment-job', to: 'pick-items', label: 'dequeue' },
        { from: 'pick-items', to: 'pack-shipment', label: '' },
        { from: 'pack-shipment', to: 'book-carrier', label: '' },
        { from: 'book-carrier', to: 'save-tracking', label: 'label' },
        { from: 'save-tracking', to: 'notify-customer', label: '' },
      ],
    },
    {
      id: 'payment-webhook',
      name: 'Payment webhook',
      summary: 'The provider confirms a capture out of band.',
      steps: [
        {
          id: 'provider-callback',
          name: 'Provider callback',
          kind: 'external',
        },
        {
          id: 'webhook-endpoint',
          name: 'POST /webhooks/payments',
          kind: 'http',
        },
        { id: 'verify-signature', name: 'Verify signature', kind: 'service' },
        { id: 'match-invoice', name: 'Match invoice', kind: 'db' },
        { id: 'mark-paid', name: 'Mark invoice paid', kind: 'service' },
        { id: 'emit-receipt', name: 'Emit receipt', kind: 'queue' },
      ],
      edges: [
        { from: 'provider-callback', to: 'webhook-endpoint', label: 'POST' },
        { from: 'webhook-endpoint', to: 'verify-signature', label: '' },
        { from: 'verify-signature', to: 'match-invoice', label: 'valid' },
        { from: 'match-invoice', to: 'mark-paid', label: '' },
        { from: 'mark-paid', to: 'emit-receipt', label: '' },
      ],
    },
  ],
}
