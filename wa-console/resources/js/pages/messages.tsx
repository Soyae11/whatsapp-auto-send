import { Head, router } from '@inertiajs/react';
import { useState } from 'react';
import { MessageDetailDialog } from '@/components/message-detail-dialog';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { messages as messagesRoute } from '@/routes';
import type { Message, MessageFilters, MessageStatus } from '@/types/message';
import type { SenderOption } from '@/types/send';

const STATUS_VARIANT: Record<
    MessageStatus,
    'default' | 'secondary' | 'destructive' | 'outline'
> = {
    queued: 'secondary',
    sending: 'secondary',
    sent: 'default',
    delivered: 'default',
    read: 'default',
    cancelled: 'outline',
    failed: 'destructive',
};

const STATUSES: MessageStatus[] = [
    'queued',
    'sending',
    'sent',
    'delivered',
    'read',
    'cancelled',
    'failed',
];

const ANY = '__any__';

export default function Messages({
    messages,
    hasMore,
    nextCursor,
    filters,
    senders,
}: {
    messages: Message[];
    hasMore: boolean;
    nextCursor: string | null;
    filters: MessageFilters;
    senders: SenderOption[];
}) {
    const [sender, setSender] = useState(filters.sender ?? ANY);
    const [to, setTo] = useState(filters.to ?? '');
    const [status, setStatus] = useState(filters.status ?? ANY);
    const [reference, setReference] = useState(filters.reference ?? '');
    const [selectedId, setSelectedId] = useState<string | null>(null);

    function applyFilters(event: React.FormEvent) {
        event.preventDefault();

        router.get(
            messagesRoute.url(),
            {
                ...(sender !== ANY ? { sender } : {}),
                ...(to ? { to } : {}),
                ...(status !== ANY ? { status } : {}),
                ...(reference ? { reference } : {}),
            },
            { preserveState: true, replace: true },
        );
    }

    function loadMore() {
        if (!nextCursor) {
            return;
        }

        router.get(
            messagesRoute.url(),
            {
                ...(sender !== ANY ? { sender } : {}),
                ...(to ? { to } : {}),
                ...(status !== ANY ? { status } : {}),
                ...(reference ? { reference } : {}),
                cursor: nextCursor,
            },
            { preserveState: true, replace: true },
        );
    }

    return (
        <>
            <Head title="Messages" />
            <div className="flex flex-1 flex-col gap-4 p-4">
                <Card>
                    <CardHeader>
                        <CardTitle>Filters</CardTitle>
                    </CardHeader>
                    <CardContent>
                        <form
                            onSubmit={applyFilters}
                            className="flex flex-wrap items-end gap-4"
                        >
                            <div className="flex flex-col gap-2">
                                <Label htmlFor="sender">Sender</Label>
                                <Select
                                    value={sender}
                                    onValueChange={setSender}
                                >
                                    <SelectTrigger id="sender" className="w-40">
                                        <SelectValue />
                                    </SelectTrigger>
                                    <SelectContent>
                                        <SelectItem value={ANY}>
                                            Any sender
                                        </SelectItem>
                                        {senders.map((option) => (
                                            <SelectItem
                                                key={option.name}
                                                value={option.name}
                                            >
                                                {option.name}
                                            </SelectItem>
                                        ))}
                                    </SelectContent>
                                </Select>
                            </div>

                            <div className="flex flex-col gap-2">
                                <Label htmlFor="status">Status</Label>
                                <Select
                                    value={status}
                                    onValueChange={setStatus}
                                >
                                    <SelectTrigger id="status" className="w-36">
                                        <SelectValue />
                                    </SelectTrigger>
                                    <SelectContent>
                                        <SelectItem value={ANY}>
                                            Any status
                                        </SelectItem>
                                        {STATUSES.map((option) => (
                                            <SelectItem
                                                key={option}
                                                value={option}
                                            >
                                                {option}
                                            </SelectItem>
                                        ))}
                                    </SelectContent>
                                </Select>
                            </div>

                            <div className="flex flex-col gap-2">
                                <Label htmlFor="to">Recipient</Label>
                                <Input
                                    id="to"
                                    placeholder="62812xxxxxxx"
                                    className="w-40"
                                    value={to}
                                    onChange={(event) =>
                                        setTo(event.target.value)
                                    }
                                />
                            </div>

                            <div className="flex flex-col gap-2">
                                <Label htmlFor="reference">Reference</Label>
                                <Input
                                    id="reference"
                                    className="w-40"
                                    value={reference}
                                    onChange={(event) =>
                                        setReference(event.target.value)
                                    }
                                />
                            </div>

                            <Button type="submit">Apply</Button>
                        </form>
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>Messages</CardTitle>
                    </CardHeader>
                    <CardContent>
                        {messages.length === 0 ? (
                            <p className="text-sm text-muted-foreground">
                                No messages match these filters.
                            </p>
                        ) : (
                            <div className="overflow-x-auto">
                                <table className="w-full text-sm">
                                    <thead>
                                        <tr className="border-b text-left">
                                            <th className="p-2 font-medium">
                                                To
                                            </th>
                                            <th className="p-2 font-medium">
                                                Sender
                                            </th>
                                            <th className="p-2 font-medium">
                                                Status
                                            </th>
                                            <th className="p-2 font-medium">
                                                Created
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {messages.map((message) => (
                                            <tr
                                                key={message.id}
                                                className="cursor-pointer border-b last:border-0 hover:bg-accent"
                                                onClick={() =>
                                                    setSelectedId(message.id)
                                                }
                                            >
                                                <td className="p-2">
                                                    {message.to}
                                                </td>
                                                <td className="p-2">
                                                    {message.sender}
                                                </td>
                                                <td className="p-2">
                                                    <Badge
                                                        variant={
                                                            STATUS_VARIANT[
                                                                message.status
                                                            ]
                                                        }
                                                    >
                                                        {message.status}
                                                    </Badge>
                                                </td>
                                                <td className="p-2">
                                                    {new Date(
                                                        message.created_at,
                                                    ).toLocaleString()}
                                                </td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </table>
                            </div>
                        )}

                        {hasMore && (
                            <div className="mt-4">
                                <Button variant="outline" onClick={loadMore}>
                                    Load more
                                </Button>
                            </div>
                        )}
                    </CardContent>
                </Card>
            </div>

            {selectedId && (
                <MessageDetailDialog
                    key={selectedId}
                    id={selectedId}
                    open={selectedId !== null}
                    onOpenChange={(open) => {
                        if (!open) {
                            setSelectedId(null);
                        }
                    }}
                />
            )}
        </>
    );
}

Messages.layout = {
    breadcrumbs: [
        {
            title: 'Messages',
            href: messagesRoute(),
        },
    ],
};
