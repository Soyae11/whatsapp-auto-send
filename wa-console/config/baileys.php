<?php

return [

    /*
    |--------------------------------------------------------------------------
    | Baileys Gateway
    |--------------------------------------------------------------------------
    |
    | wa-console holds no session data of its own — baileys is the source of truth.
    | The console credential is scoped to "read" + "manage" and deliberately cannot
    | "send": a send that skipped the dispatcher would also skip the pacing that
    | keeps a number from being banned. See ../baileys/src/auth.ts.
    |
    */

    'base_url' => env('BAILEYS_BASE_URL', 'http://127.0.0.1:3000'),

    'console_key' => env('BAILEYS_CONSOLE_KEY'),

];
