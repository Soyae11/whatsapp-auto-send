<?php

namespace App\Http\Controllers;

use App\Services\WaAdmin\WaAdminClient;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;
use Inertia\Inertia;
use Inertia\Response;

class ApiKeysController extends Controller
{
    public function __construct(private readonly WaAdminClient $admin) {}

    public function index(): Response
    {
        $keys = [];
        $senders = [];

        try {
            $keys = $this->admin->listKeys($this->ownerId());
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        try {
            $senders = $this->admin->listSenders($this->ownerId());
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        return Inertia::render('api-keys', [
            'keys' => $keys,
            'senders' => $senders,
        ]);
    }

    public function store(Request $request): RedirectResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:200'],
            'project' => ['required', 'string', 'max:200'],
            'environment' => ['required', 'string', 'in:live,test'],
            'senders' => ['required', 'array', 'min:1'],
            'senders.*' => ['required', 'string'],
            'rate_limit' => ['nullable', 'integer', 'min:1'],
        ]);

        $created = $this->admin->createKey([
            'name' => $data['name'],
            'project' => $data['project'],
            'environment' => $data['environment'],
            'senders' => $data['senders'],
            'rate_limit' => $data['rate_limit'] ?? 600,
        ], $this->ownerId());

        Inertia::flash('toast', [
            'type' => 'success',
            'message' => 'Key created. Copy the secret now — it will not be shown again.',
        ]);
        Inertia::flash('newKey', $created);

        return back();
    }

    public function revoke(string $id): RedirectResponse
    {
        $this->admin->revokeKey($id, $this->ownerId());

        Inertia::flash('toast', ['type' => 'success', 'message' => 'Key revoked.']);

        return back();
    }

    private function ownerId(): string
    {
        return (string) Auth::id();
    }
}
