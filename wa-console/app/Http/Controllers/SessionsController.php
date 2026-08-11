<?php

namespace App\Http\Controllers;

use App\Services\Baileys\BaileysClient;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;
use Inertia\Inertia;
use Inertia\Response;

class SessionsController extends Controller
{
    public function __construct(private readonly BaileysClient $baileys) {}

    public function index(): Response
    {
        return Inertia::render('dashboard', [
            'sessions' => $this->baileys->list($this->ownerId()),
        ]);
    }

    public function store(Request $request): RedirectResponse
    {
        $data = $request->validate([
            'label' => ['required', 'string', 'max:200'],
        ]);

        $this->baileys->create($data['label'], $this->ownerId());

        Inertia::flash('toast', ['type' => 'success', 'message' => __('Session created.')]);

        return back();
    }

    public function connect(string $session): JsonResponse
    {
        return response()->json($this->baileys->connect($session, $this->ownerId()));
    }

    public function qr(string $session): JsonResponse
    {
        return response()->json($this->baileys->qr($session, $this->ownerId()));
    }

    public function pair(string $session, Request $request): JsonResponse
    {
        $data = $request->validate([
            'phoneNumber' => ['required', 'string'],
        ]);

        return response()->json($this->baileys->pair($session, $data['phoneNumber'], $this->ownerId()));
    }

    public function logout(string $session): RedirectResponse
    {
        $this->baileys->logout($session, $this->ownerId());
        $this->baileys->reset($session, $this->ownerId());

        Inertia::flash('toast', ['type' => 'success', 'message' => __('Session logged out. Ready to pair again.')]);

        return back();
    }

    public function destroy(string $session): RedirectResponse
    {
        $this->baileys->delete($session, $this->ownerId());

        Inertia::flash('toast', ['type' => 'success', 'message' => __('Session removed.')]);

        return back();
    }

    private function ownerId(): string
    {
        return (string) Auth::id();
    }
}
