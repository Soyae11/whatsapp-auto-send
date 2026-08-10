<?php

namespace App\Http\Controllers;

use App\Services\Baileys\BaileysClient;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Inertia\Inertia;
use Inertia\Response;

class SessionsController extends Controller
{
    public function __construct(private readonly BaileysClient $baileys) {}

    public function index(): Response
    {
        return Inertia::render('dashboard', [
            'sessions' => $this->baileys->list(),
        ]);
    }

    public function store(Request $request): RedirectResponse
    {
        $data = $request->validate([
            'label' => ['required', 'string', 'max:200'],
        ]);

        $this->baileys->create($data['label']);

        Inertia::flash('toast', ['type' => 'success', 'message' => __('Session created.')]);

        return back();
    }

    public function connect(string $session): JsonResponse
    {
        return response()->json($this->baileys->connect($session));
    }

    public function qr(string $session): JsonResponse
    {
        return response()->json($this->baileys->qr($session));
    }

    public function pair(string $session, Request $request): JsonResponse
    {
        $data = $request->validate([
            'phoneNumber' => ['required', 'string'],
        ]);

        return response()->json($this->baileys->pair($session, $data['phoneNumber']));
    }

    public function logout(string $session): RedirectResponse
    {
        $this->baileys->logout($session);

        Inertia::flash('toast', ['type' => 'success', 'message' => __('Session logged out.')]);

        return back();
    }

    public function destroy(string $session): RedirectResponse
    {
        $this->baileys->delete($session);

        Inertia::flash('toast', ['type' => 'success', 'message' => __('Session removed.')]);

        return back();
    }
}
