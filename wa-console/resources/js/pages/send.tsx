import { Head, useForm } from '@inertiajs/react';
import { store } from '@/actions/App/Http/Controllers/SendController';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Label } from '@/components/ui/label';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Spinner } from '@/components/ui/spinner';
import { Textarea } from '@/components/ui/textarea';
import { send } from '@/routes';
import type { SenderOption, SendResult } from '@/types/send';

export default function Send({
    senders,
    results,
}: {
    senders: SenderOption[];
    results: SendResult[] | null;
}) {
    const { data, setData, post, processing, errors, reset } = useForm({
        sender: senders[0]?.name ?? '',
        numbers: '',
        message: '',
        critical: false,
    });

    function submit(event: React.FormEvent) {
        event.preventDefault();
        post(store.url(), {
            onSuccess: () => reset('numbers', 'message', 'critical'),
        });
    }

    return (
        <>
            <Head title="Send" />
            <div className="flex flex-1 flex-col gap-4 p-4">
                <Card>
                    <CardHeader>
                        <CardTitle>Send a message</CardTitle>
                    </CardHeader>
                    <CardContent>
                        {senders.length === 0 ? (
                            <p className="text-sm text-muted-foreground">
                                No senders available for this key. Check
                                wa-consumer-api's sender configuration.
                            </p>
                        ) : (
                            <form
                                onSubmit={submit}
                                className="flex max-w-xl flex-col gap-4"
                            >
                                <div className="flex flex-col gap-2">
                                    <Label htmlFor="sender">Sender</Label>
                                    <Select
                                        value={data.sender}
                                        onValueChange={(value) =>
                                            setData('sender', value)
                                        }
                                    >
                                        <SelectTrigger id="sender">
                                            <SelectValue placeholder="Choose a session" />
                                        </SelectTrigger>
                                        <SelectContent>
                                            {senders.map((sender) => (
                                                <SelectItem
                                                    key={sender.name}
                                                    value={sender.name}
                                                    disabled={!sender.accepting}
                                                >
                                                    {sender.name}
                                                    {sender.health !==
                                                    'available'
                                                        ? ` (${sender.health})`
                                                        : ''}
                                                    <span>
                                                        <Badge
                                                            variant={
                                                                sender.mode ===
                                                                'pool'
                                                                    ? 'secondary'
                                                                    : 'outline'
                                                            }
                                                        >
                                                            {sender.mode ===
                                                            'pool'
                                                                ? 'Pool'
                                                                : 'Session'}
                                                        </Badge>
                                                    </span>
                                                </SelectItem>
                                            ))}
                                        </SelectContent>
                                    </Select>
                                    {errors.sender && (
                                        <p className="text-sm text-destructive">
                                            {errors.sender}
                                        </p>
                                    )}
                                </div>

                                <div className="flex flex-col gap-2">
                                    <Label htmlFor="numbers">Numbers</Label>
                                    <Textarea
                                        id="numbers"
                                        placeholder={
                                            '62812xxxxxxx\n62813xxxxxxx'
                                        }
                                        rows={4}
                                        value={data.numbers}
                                        onChange={(event) =>
                                            setData(
                                                'numbers',
                                                event.target.value,
                                            )
                                        }
                                    />
                                    <p className="text-sm text-muted-foreground">
                                        One number per line, or comma-separated.
                                        Duplicates are ignored.
                                    </p>
                                    {errors.numbers && (
                                        <p className="text-sm text-destructive">
                                            {errors.numbers}
                                        </p>
                                    )}
                                </div>

                                <div className="flex flex-col gap-2">
                                    <Label htmlFor="message">Message</Label>
                                    <Textarea
                                        id="message"
                                        rows={6}
                                        value={data.message}
                                        onChange={(event) =>
                                            setData(
                                                'message',
                                                event.target.value,
                                            )
                                        }
                                    />
                                    {errors.message && (
                                        <p className="text-sm text-destructive">
                                            {errors.message}
                                        </p>
                                    )}
                                </div>

                                <label className="flex items-center gap-2 text-sm">
                                    <Checkbox
                                        checked={data.critical}
                                        onCheckedChange={(checked) =>
                                            setData('critical', checked === true)
                                        }
                                    />
                                    Critical
                                </label>
                                <p className="-mt-2 text-sm text-muted-foreground">
                                    Sends as soon as pacing allows instead of
                                    waiting behind the normal queue. Reserve
                                    this for messages that are actually
                                    time-sensitive — everything critical still
                                    competes for the same WhatsApp number.
                                </p>
                                {errors.critical && (
                                    <p className="text-sm text-destructive">
                                        {errors.critical}
                                    </p>
                                )}

                                <Button
                                    type="submit"
                                    disabled={processing}
                                    className="self-start"
                                >
                                    {processing ? (
                                        <Spinner className="size-4" />
                                    ) : (
                                        'Send'
                                    )}
                                </Button>
                            </form>
                        )}
                    </CardContent>
                </Card>

                {results && results.length > 0 && (
                    <Card>
                        <CardHeader>
                            <CardTitle>Last batch</CardTitle>
                        </CardHeader>
                        <CardContent>
                            <div className="overflow-x-auto">
                                <table className="w-full text-sm">
                                    <thead>
                                        <tr className="border-b text-left">
                                            <th className="p-2 font-medium">
                                                Number
                                            </th>
                                            <th className="p-2 font-medium">
                                                Status
                                            </th>
                                            <th className="p-2 font-medium">
                                                Detail
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {results.map((result) => (
                                            <tr
                                                key={result.number}
                                                className="border-b last:border-0"
                                            >
                                                <td className="p-2">
                                                    {result.number}
                                                </td>
                                                <td className="p-2">
                                                    <Badge
                                                        variant={
                                                            result.status ===
                                                            'accepted'
                                                                ? 'default'
                                                                : 'destructive'
                                                        }
                                                    >
                                                        {result.status}
                                                    </Badge>
                                                </td>
                                                <td className="p-2">
                                                    {result.error ?? result.id}
                                                </td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </table>
                            </div>
                        </CardContent>
                    </Card>
                )}
            </div>
        </>
    );
}

Send.layout = {
    breadcrumbs: [
        {
            title: 'Send',
            href: send(),
        },
    ],
};
