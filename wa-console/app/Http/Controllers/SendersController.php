<?php

namespace App\Http\Controllers;

use App\Services\Baileys\BaileysClient;
use App\Services\WaAdmin\WaAdminClient;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;
use Inertia\Inertia;
use Inertia\Response;

class SendersController extends Controller
{
    public function __construct(
        private readonly WaAdminClient $admin,
        private readonly BaileysClient $baileys,
    ) {}

    public function index(): Response
    {
        $senders = [];
        $sessions = [];

        try {
            $senders = $this->admin->listSenders($this->ownerId());
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        try {
            $sessions = array_map(fn ($session) => [
                'id' => $session['id'],
                'label' => $session['label'],
                'phoneNumber' => $session['phoneNumber'] ?? null,
            ], $this->baileys->list($this->ownerId()));
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        return Inertia::render('senders', [
            'senders' => $senders,
            'sessions' => $sessions,
        ]);
    }

    public function store(Request $request): RedirectResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:64', 'regex:/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/'],
            'mode' => ['required', 'string', 'in:single,pool'],
            'session_id' => ['required_if:mode,single', 'nullable', 'string'],
        ]);

        $this->admin->createSender(
            $data['name'],
            $data['mode'],
            $data['mode'] === 'single' ? $data['session_id'] : null,
            $this->ownerId(),
        );

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Sender "'.$data['name'].'" created.']);

        return back();
    }

    public function destroy(string $sender): RedirectResponse
    {
        $this->admin->deleteSender($sender, $this->ownerId());

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Sender "'.$sender.'" removed.']);

        return back();
    }

    private function ownerId(): string
    {
        return (string) Auth::id();
    }
}
