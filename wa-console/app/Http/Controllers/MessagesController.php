<?php

namespace App\Http\Controllers;

use App\Http\Controllers\Concerns\ListsSenders;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Inertia\Inertia;
use Inertia\Response;
use Wa\Laravel\Contracts\WaClient;

class MessagesController extends Controller
{
    use ListsSenders;

    public function __construct(private readonly WaClient $wa) {}

    public function index(Request $request): Response
    {
        $filters = $request->only(['sender', 'to', 'status', 'reference', 'created_after', 'created_before']);
        $filters = array_filter($filters, fn ($value) => $value !== null && $value !== '');

        $query = [...$filters, 'limit' => 25];
        if ($cursor = $request->query('cursor')) {
            $query['cursor'] = $cursor;
        }

        $messages = [];
        $hasMore = false;
        $nextCursor = null;

        try {
            $page = $this->wa->messages($query);
            $messages = array_map(fn ($message) => $message->raw, $page->data);
            $hasMore = $page->hasMore;
            $nextCursor = $page->nextCursor;
        } catch (\Throwable $e) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $e->getMessage()]);
        }

        return Inertia::render('messages', [
            'messages' => $messages,
            'hasMore' => $hasMore,
            'nextCursor' => $nextCursor,
            'filters' => $filters,
            'senders' => $this->senderOptions(),
        ]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json($this->wa->find($id)->raw);
    }
}
