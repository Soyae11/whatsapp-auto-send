<?php

namespace App\Http\Controllers;

use App\Http\Controllers\Concerns\ListsSenders;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
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
        $data = $request->validate([
            'sender' => ['required', 'string'],
            'numbers' => ['required', 'string'],
            'message' => ['required', 'string', 'max:65536'],
        ]);

        $numbers = collect(preg_split('/[\r\n,]+/', $data['numbers']))
            ->map(fn (string $number) => trim($number))
            ->filter()
            ->unique()
            ->values();

        if ($numbers->isEmpty()) {
            return back()->withErrors(['numbers' => 'Enter at least one number.']);
        }

        $pending = $numbers->map(fn (string $number) => $this->wa->to($number)
            ->from($data['sender'])
            ->text($data['message'])
            ->key((string) Str::uuid()));

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
