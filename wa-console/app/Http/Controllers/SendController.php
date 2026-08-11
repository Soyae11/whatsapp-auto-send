<?php

namespace App\Http\Controllers;

use App\Http\Controllers\Concerns\ListsSenders;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Inertia\Response;
use Wa\Laravel\Contracts\WaClient;

class SendController extends Controller
{
    use ListsSenders;

    public function __construct(private readonly WaClient $wa) {}

    public function index(): Response
    {
        return Inertia::render('send', [
            'senders' => $this->senderOptions(),
            'results' => session('results'),
        ]);
    }

    public function store(Request $request): RedirectResponse
    {
        // senderOptions() is already filtered to the current user's own senders (see
        // ListsSenders) — Rule::in against it is what actually stops a POST naming another
        // user's sender, not just hiding it from the dropdown. wa-console's shared WA_KEY
        // would happily authorise the send otherwise, since Go only checks the key's own
        // sender grant, not who owns the sender.
        $ownedSenders = array_column($this->senderOptions(), 'name');

        $data = $request->validate([
            'sender' => ['required', 'string', Rule::in($ownedSenders)],
            'numbers' => ['required', 'string'],
            'message' => ['required', 'string', 'max:65536'],
            'critical' => ['sometimes', 'boolean'],
        ]);

        $numbers = collect(preg_split('/[\r\n,]+/', $data['numbers']))
            ->map(fn (string $number) => trim($number))
            ->filter()
            ->unique()
            ->values();

        if ($numbers->isEmpty()) {
            return back()->withErrors(['numbers' => 'Enter at least one number.']);
        }

        $critical = $data['critical'] ?? false;

        $pending = $numbers->map(function (string $number) use ($data, $critical) {
            $message = $this->wa->to($number)
                ->from($data['sender'])
                ->text($data['message'])
                ->key((string) Str::uuid());

            return $critical ? $message->critical() : $message;
        });

        $result = $this->wa->sendMany($pending);

        $results = $numbers->values()->map(function (string $number, int $i) use ($result) {
            if (isset($result->accepted[$i])) {
                return ['number' => $number, 'status' => 'accepted', 'id' => $result->accepted[$i]->id];
            }

            return ['number' => $number, 'status' => 'rejected', 'error' => $result->rejected[$i]?->getMessage()];
        })->all();

        session()->flash('results', $results);

        Inertia::flash('toast', [
            'type' => $result->allAccepted() ? 'success' : 'warning',
            'message' => "{$result->acceptedCount()} queued, {$result->rejectedCount()} failed.",
        ]);

        return back();
    }
}
