# wa/laravel

Laravel SDK for the dispatcher API. Send a WhatsApp message to an employee, and know
whether it arrived.

Part of the stack in the parent directory — see [`../README.md`](../README.md) for what it
does and how the pieces fit together.

This is a library, not a service. There is nothing to start; you install it into a Laravel
app.

## Install it

```sh
composer require wa/laravel
php artisan vendor:publish --tag=wa-config
```

```dotenv
WA_URL=http://localhost:8080
WA_KEY=wsk_live_...
WA_SENDER=hr-notifications
WA_WEBHOOK_SECRET=whsec_...
```

`WA_URL` is the dispatcher. `WA_KEY` is a consumer API key — mint one with
`../dispatcher/bin/wa-admin`. The service provider and the `Wa` facade are auto-discovered.

## Use it

```php
use Wa\Laravel\Facades\Wa;

$message = Wa::to($employee->phone)
    ->from(config('wa.senders.hr'))
    ->text('Cuti Anda telah disetujui.')
    ->reference("leave_request:{$leave->id}")
    ->key("leave-{$leave->id}-approved-{$employee->id}")
    ->send();
```

Full reference at http://localhost:8080/docs/ once the stack is up.

## Two things that will bite you

**`send()` returns a queued message, not a delivered one.** The dispatcher paces sends so
WhatsApp does not ban the number. Never treat a successful `send()` as proof that anyone
was notified — check `$message->status`.

**Every send needs an idempotency key, and the package will not let you send without one.**
Laravel retries queued jobs by default; without a key, a retry messages a real person
twice. Derive it from your own data so a retry recomputes the same string —
`Str::uuid()` at call time does not help.

## Develop it

Sends are dry runs everywhere except `APP_ENV=production`. Override with `WA_DRY_RUN`, or
per message with `->dryRun()`. In tests, use `Wa::fake()`.
