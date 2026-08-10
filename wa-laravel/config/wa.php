<?php

return [

    'url' => env('WA_URL', 'https://wa.internal'),

    'key' => env('WA_KEY'),

    'senders' => [
        'default' => env('WA_SENDER'),
    ],

    'dry_run' => env('WA_DRY_RUN'),

    'timeout' => (int) env('WA_TIMEOUT', 10),

    'connect_timeout' => (int) env('WA_CONNECT_TIMEOUT', 5),

    'webhooks' => [
        'secret' => env('WA_WEBHOOK_SECRET'),

        'tolerance' => (int) env('WA_WEBHOOK_TOLERANCE', 300),
    ],

];
