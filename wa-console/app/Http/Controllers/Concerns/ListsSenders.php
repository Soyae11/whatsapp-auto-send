<?php

namespace App\Http\Controllers\Concerns;

use Inertia\Inertia;

trait ListsSenders
{
    protected function senderOptions(): array
    {
        try {
            return array_map(fn ($sender) => [
                'name' => $sender->name,
                'health' => $sender->health,
                'accepting' => $sender->accepting,
            ], $this->wa->senders());
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);

            return [];
        }
    }
}
