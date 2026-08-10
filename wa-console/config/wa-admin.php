<?php

return [

    /*
    |--------------------------------------------------------------------------
    | wa-consumer-api admin surface
    |--------------------------------------------------------------------------
    |
    | Pool management (main/backup session rotation) is an operator action, not a send, so it
    | uses wa-consumer-api's ADMIN_API_KEY — a different, more privileged credential than
    | wa-laravel's WA_KEY (which is deliberately scoped to sending only). Never used for
    | sending; only for the /internal/* pool routes.
    |
    */

    'base_url' => env('WA_ADMIN_URL', env('WA_URL', 'http://127.0.0.1:8080')),

    'admin_key' => env('WA_ADMIN_KEY'),

];
