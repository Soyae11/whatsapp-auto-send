<?php

namespace App\Http\Controllers\Concerns;

use Illuminate\Support\Facades\Auth;
use Inertia\Inertia;

trait ListsSenders
{
    /**
     * Senders the current user owns, out of everything wa-console's own WA_KEY may send from.
     * owner_id here is metadata the Go API attaches for display only — it does not grant
     * access by itself, so filtering here is what actually keeps one user from seeing (or
     * picking, on the Send page) another user's senders.
     */
    protected function senderOptions(): array
    {
        $ownerId = (string) Auth::id();

        try {
            $owned = array_filter($this->wa->senders(), fn ($sender) => $sender->ownerId === $ownerId);

            return array_values(array_map(fn ($sender) => [
                'name' => $sender->name,
                'mode' => $sender->mode,
                'health' => $sender->health,
                'accepting' => $sender->accepting,
            ], $owned));
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);

            return [];
        }
    }
}
