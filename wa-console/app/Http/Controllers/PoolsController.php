<?php

namespace App\Http\Controllers;

use App\Http\Controllers\Concerns\ListsSenders;
use App\Services\Baileys\BaileysClient;
use App\Services\WaAdmin\WaAdminClient;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Inertia\Inertia;
use Inertia\Response;
use Wa\Laravel\Contracts\WaClient;

class PoolsController extends Controller
{
    use ListsSenders;

    public function __construct(
        private readonly WaClient $wa,
        private readonly WaAdminClient $admin,
        private readonly BaileysClient $baileys,
    ) {}

    public function index(): Response
    {
        $senders = $this->senderOptions();
        $pools = [];

        foreach ($senders as $sender) {
            try {
                $pools[$sender['name']] = $this->admin->pool($sender['name'])['members'];
            } catch (\Throwable $e) {
                $pools[$sender['name']] = [];
                Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
            }
        }

        $sessions = [];

        try {
            $sessions = array_map(fn ($session) => [
                'id' => $session['id'],
                'label' => $session['label'],
                'phoneNumber' => $session['phoneNumber'] ?? null,
            ], $this->baileys->list());
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        return Inertia::render('pools', [
            'senders' => $senders,
            'pools' => $pools,
            'sessions' => $sessions,
        ]);
    }

    public function store(Request $request): RedirectResponse
    {
        $data = $request->validate([
            'sender' => ['required', 'string'],
            'sessions' => ['required', 'array', 'min:1'],
            'sessions.*' => ['required', 'string'],
        ]);

        $this->admin->createPool($data['sender'], $data['sessions']);

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Pool created for '.$data['sender'].'.']);

        return back();
    }

    public function destroy(string $sender): RedirectResponse
    {
        $this->admin->deletePool($sender);

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Pool deleted for '.$sender.'.']);

        return back();
    }

    public function addMember(Request $request, string $sender): RedirectResponse
    {
        $data = $request->validate(['session_id' => ['required', 'string']]);

        $this->admin->addMember($sender, $data['session_id']);

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Backup added to '.$sender.'.']);

        return back();
    }

    public function removeMember(string $sender, string $sessionId): RedirectResponse
    {
        $this->admin->removeMember($sender, $sessionId);

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Session removed from '.$sender.'.']);

        return back();
    }

    public function promote(Request $request, string $sender): RedirectResponse
    {
        $data = $request->validate(['session_id' => ['required', 'string']]);

        $this->admin->promote($sender, $data['session_id']);

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Promoted to main for '.$sender.'.']);

        return back();
    }

    public function reinstate(string $sender, string $sessionId): RedirectResponse
    {
        $this->admin->reinstate($sender, $sessionId);

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Session reinstated for '.$sender.'.']);

        return back();
    }
}
