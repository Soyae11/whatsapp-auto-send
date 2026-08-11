<?php

namespace App\Http\Controllers;

use App\Http\Controllers\Concerns\ListsSenders;
use App\Services\Baileys\BaileysClient;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;
use Inertia\Inertia;
use Inertia\Response;
use Wa\Laravel\Contracts\WaClient;

class MessagesController extends Controller
{
    use ListsSenders;

    public function __construct(
        private readonly WaClient $wa,
        private readonly BaileysClient $baileys,
    ) {}

    public function index(Request $request): Response
    {
        $filters = $request->only(['sender', 'to', 'status', 'reference', 'created_after', 'created_before']);
        $filters = array_filter($filters, fn ($value) => $value !== null && $value !== '');

        $senders = $this->senderOptions();
        $ownedSenderNames = array_column($senders, 'name');

        // An explicit filter naming a sender the caller doesn't own is dropped rather than
        // honoured, so it never reaches the frontend's filter form either.
        if (isset($filters['sender']) && ! in_array($filters['sender'], $ownedSenderNames, true)) {
            unset($filters['sender']);
        }

        // wa-console's own WA_KEY is shared by every user, so /v1/messages on its own only
        // scopes to "this wa-console install", not to the individual user asking. With no
        // explicit sender filter, the query sent to the API defaults to every sender the
        // caller owns (comma-separated, OR semantics — see MessageFilter.Senders in
        // wa-shared/store/messages.go) rather than silently falling back to "everyone's
        // messages". This default never leaks into $filters below — the frontend's "Any
        // sender" option should keep meaning "unset", not the expanded owner list.
        $apiQuery = [...$filters, 'limit' => 25];
        if (! isset($apiQuery['sender'])) {
            $apiQuery['sender'] = implode(',', $ownedSenderNames);
        }
        if ($cursor = $request->query('cursor')) {
            $apiQuery['cursor'] = $cursor;
        }

        $messages = [];
        $hasMore = false;
        $nextCursor = null;

        // No owned senders means no messages could possibly be this user's — skip the call
        // rather than send an empty "sender" filter, which the API reads as no filter at all.
        if ($ownedSenderNames !== []) {
            try {
                $page = $this->wa->messages($apiQuery);
                $messages = array_map(fn ($message) => $message->raw, $page->data);
                $hasMore = $page->hasMore;
                $nextCursor = $page->nextCursor;
            } catch (\Throwable $e) {
                Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
            }
        }

        $sessions = [];

        try {
            $sessions = array_map(fn ($session) => [
                'id' => $session['id'],
                'label' => $session['label'],
                'phoneNumber' => $session['phoneNumber'] ?? null,
            ], $this->baileys->list((string) Auth::id()));
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        return Inertia::render('messages', [
            'messages' => $messages,
            'hasMore' => $hasMore,
            'nextCursor' => $nextCursor,
            'filters' => $filters,
            'senders' => $senders,
            'sessions' => $sessions,
        ]);
    }

    public function show(string $id): JsonResponse
    {
        $message = $this->wa->find($id)->raw;

        $ownedSenderNames = array_column($this->senderOptions(), 'name');
        abort_unless(in_array($message['sender'] ?? null, $ownedSenderNames, true), 404);

        return response()->json($message);
    }
}
