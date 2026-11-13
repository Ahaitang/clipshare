package work.metoak.clipshare.sender;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Bundle;
import android.text.InputType;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.security.MessageDigest;
import java.util.ArrayList;
import java.util.List;

/**
 * clipshare 发送端：只把文本上行到局域网内的 clipshare 节点，不接收、不监听、不落历史。
 *
 * 为什么是「只发送」：
 *   桌面端是靠 mDNS / seeds 探活（GET /v1/health）来发现彼此的，本 App 不起 HTTP 服务，
 *   所以桌面端**根本发现不了它**，也就不会往它推内容。这里的「只发送」不是靠开关，
 *   而是靠它压根没有可被发现的入口 —— 这样最不容易出错。
 *
 * 协议与桌面端完全一致：POST /v1/items，头里带 Authorization: Bearer <roomKey>
 * 与 X-Room: sha256(roomKey)[:12]，body 是 Entry JSON。
 */
public class MainActivity extends Activity {

    private static final String PREFS = "clipshare";
    private static final String KEY_INVITE = "invite";

    private EditText inviteInput;
    private EditText textInput;
    private TextView statusView;
    private Button sendButton;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(buildUi());

        SharedPreferences sp = getSharedPreferences(PREFS, MODE_PRIVATE);
        inviteInput.setText(sp.getString(KEY_INVITE, ""));

        handleIntent(getIntent());
    }

    /** Activity 复用时（singleTop）也要处理新 intent。 */
    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handleIntent(intent);
    }

    /**
     * 处理两种外部输入：
     *   1. 分享文本（ACTION_SEND）—— 手机上最省事的入口；
     *   2. 预置邀请码（extra "invite"）—— 供二维码/深链/自动化配置使用，
     *      没有它就只能在界面上手输邀请码。
     */
    private void handleIntent(Intent intent) {
        if (intent == null) {
            return;
        }
        String presetInvite = intent.getStringExtra("invite");
        if (presetInvite != null && !presetInvite.trim().isEmpty()) {
            inviteInput.setText(presetInvite.trim());
            getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                    .putString(KEY_INVITE, presetInvite.trim()).apply();
            setStatus("已通过 Intent 预置邀请码");
        }
        if (Intent.ACTION_SEND.equals(intent.getAction())) {
            String shared = intent.getStringExtra(Intent.EXTRA_TEXT);
            if (shared != null && shared.length() > 0) {
                textInput.setText(shared);
                maybeAutoSend();
            }
        }
    }

    private View buildUi() {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        int pad = dp(16);
        root.setPadding(pad, pad, pad, pad);

        TextView title = new TextView(this);
        title.setText("clipshare 发送端（只发送，不接收）");
        title.setTextSize(18);
        root.addView(title);

        TextView hint = new TextView(this);
        hint.setText("把文本发到局域网内同一房间的其它机器。本 App 不接收别人的剪贴板，"
                + "也不会出现在对方的节点列表里。");
        hint.setTextSize(12);
        hint.setPadding(0, dp(6), 0, dp(12));
        root.addView(hint);

        TextView inviteLabel = new TextView(this);
        inviteLabel.setText("邀请码（在电脑上执行 clipshare invite 得到）");
        inviteLabel.setTextSize(12);
        root.addView(inviteLabel);

        inviteInput = new EditText(this);
        inviteInput.setHint("clipshare1:房间码@192.168.1.7:47100");
        inviteInput.setTextSize(13);
        inviteInput.setSingleLine(true);
        inviteInput.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
        root.addView(inviteInput);

        TextView textLabel = new TextView(this);
        textLabel.setText("要发送的内容");
        textLabel.setTextSize(12);
        textLabel.setPadding(0, dp(12), 0, 0);
        root.addView(textLabel);

        textInput = new EditText(this);
        textInput.setHint("在这里输入或粘贴文本");
        textInput.setTextSize(15);
        textInput.setMinLines(4);
        textInput.setGravity(android.view.Gravity.TOP | android.view.Gravity.START);
        textInput.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE);
        root.addView(textInput, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f));

        LinearLayout buttons = new LinearLayout(this);
        buttons.setOrientation(LinearLayout.HORIZONTAL);
        buttons.setPadding(0, dp(10), 0, 0);

        Button paste = new Button(this);
        paste.setText("读系统剪贴板");
        paste.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                ClipboardManager cm = (ClipboardManager) getSystemService(Context.CLIPBOARD_SERVICE);
                if (cm != null && cm.hasPrimaryClip() && cm.getPrimaryClip().getItemCount() > 0) {
                    CharSequence text = cm.getPrimaryClip().getItemAt(0).coerceToText(MainActivity.this);
                    textInput.setText(text);
                    setStatus("已读取系统剪贴板（" + text.length() + " 字）");
                } else {
                    setStatus("系统剪贴板是空的");
                }
            }
        });
        buttons.addView(paste, new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f));

        sendButton = new Button(this);
        sendButton.setText("发送");
        sendButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                doSend(false);
            }
        });
        buttons.addView(sendButton, new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f));
        root.addView(buttons);

        statusView = new TextView(this);
        statusView.setTextSize(12);
        statusView.setPadding(0, dp(10), 0, 0);
        root.addView(statusView);

        ScrollView scroll = new ScrollView(this);
        scroll.addView(root);
        return scroll;
    }

    /** 分享进来时自动发送；没有保存过邀请码就只填内容，等用户补邀请码。 */
    private void maybeAutoSend() {
        if (inviteInput.getText().toString().trim().isEmpty()) {
            setStatus("已填入分享的文本；请先填邀请码再点发送");
            return;
        }
        doSend(true);
    }

    private void doSend(final boolean auto) {
        final String invite = inviteInput.getText().toString().trim();
        final String content = textInput.getText().toString();
        if (invite.isEmpty()) {
            setStatus("请先填邀请码（电脑上执行 clipshare invite）");
            return;
        }
        if (content.trim().isEmpty()) {
            setStatus("没有要发送的内容");
            return;
        }

        final ParsedInvite parsed;
        try {
            parsed = ParsedInvite.parse(invite);
        } catch (IllegalArgumentException e) {
            setStatus("邀请码无效：" + e.getMessage());
            return;
        }

        getSharedPreferences(PREFS, MODE_PRIVATE).edit().putString(KEY_INVITE, invite).apply();
        sendButton.setEnabled(false);
        setStatus("正在发送到 " + parsed.seeds.size() + " 个地址…");

        new Thread(new Runnable() {
            @Override
            public void run() {
                String result;
                try {
                    String okSeed = sendToAny(parsed, content);
                    result = "已发送到 " + okSeed + (auto ? "（来自分享）" : "");
                } catch (IOException e) {
                    result = "发送失败：" + e.getMessage();
                }
                final String finalResult = result;
                runOnUiThread(new Runnable() {
                    @Override
                    public void run() {
                        sendButton.setEnabled(true);
                        setStatus(finalResult);
                    }
                });
            }
        }).start();
    }

    /** 逐个地址尝试，任何一个成功即视为发送成功。 */
    private String sendToAny(ParsedInvite inv, String content) throws IOException {
        String lastError = "没有可用地址";
        for (String seed : inv.seeds) {
            try {
                postEntry(seed, inv.roomKey, content);
                return seed;
            } catch (IOException e) {
                lastError = seed + "：" + e.getMessage();
            }
        }
        throw new IOException(lastError);
    }

    /** 与桌面端 sync.Server 对齐的请求形态。 */
    private void postEntry(String seed, String roomKey, String content) throws IOException {
        URL url = new URL("http://" + seed + "/v1/items");
        HttpURLConnection conn = (HttpURLConnection) url.openConnection();
        try {
            conn.setRequestMethod("POST");
            conn.setConnectTimeout(4000);
            conn.setReadTimeout(4000);
            conn.setDoOutput(true);
            conn.setRequestProperty("Content-Type", "application/json; charset=utf-8");
            conn.setRequestProperty("Authorization", "Bearer " + roomKey);
            conn.setRequestProperty("X-Room", ParsedInvite.roomHash(roomKey));

            JSONObject body = new JSONObject();
            try {
                body.put("content", content);
                // origin/originName 让电脑端历史里显示「来自 手机」而不是本地复制
                body.put("origin", "android");
                body.put("originName", "手机");
            } catch (Exception e) {
                throw new IOException("组装请求失败: " + e.getMessage());
            }

            byte[] payload = body.toString().getBytes("UTF-8");
            OutputStream out = conn.getOutputStream();
            out.write(payload);
            out.flush();
            out.close();

            int code = conn.getResponseCode();
            if (code == 202 || code == 200) {
                return;
            }
            String detail = readSnippet(conn);
            throw new IOException("HTTP " + code + (detail.isEmpty() ? "" : " " + detail));
        } finally {
            conn.disconnect();
        }
    }

    private static String readSnippet(HttpURLConnection conn) {
        try {
            InputStream in = conn.getErrorStream();
            if (in == null) {
                return "";
            }
            ByteArrayOutputStream buf = new ByteArrayOutputStream();
            byte[] tmp = new byte[512];
            int n;
            while ((n = in.read(tmp)) > 0) {
                buf.write(tmp, 0, n);
            }
            in.close();
            return buf.toString("UTF-8").trim();
        } catch (IOException e) {
            return "";
        }
    }

    private void setStatus(String text) {
        statusView.setText(text);
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }

    /** 邀请码解析：与桌面端 onboard 包同一格式 clipshare1:<roomKey>@host:port[,host:port] */
    static final class ParsedInvite {
        final String roomKey;
        final List<String> seeds;

        private ParsedInvite(String roomKey, List<String> seeds) {
            this.roomKey = roomKey;
            this.seeds = seeds;
        }

        static ParsedInvite parse(String code) {
            String trimmed = code.trim();
            if (!trimmed.toLowerCase().startsWith("clipshare1:")) {
                throw new IllegalArgumentException("应以 clipshare1: 开头");
            }
            String body = trimmed.substring("clipshare1:".length()).trim();
            String roomKey = body;
            List<String> seeds = new ArrayList<String>();
            int at = body.indexOf('@');
            if (at >= 0) {
                roomKey = body.substring(0, at).trim();
                String rest = body.substring(at + 1);
                for (String part : rest.split(",")) {
                    String s = part.trim();
                    if (!s.isEmpty()) {
                        seeds.add(s);
                    }
                }
            }
            if (roomKey.isEmpty()) {
                throw new IllegalArgumentException("缺少房间码");
            }
            if (seeds.isEmpty()) {
                throw new IllegalArgumentException("缺少地址（应为 clipshare1:房间码@IP:端口）");
            }
            return new ParsedInvite(roomKey, seeds);
        }

        /** 与桌面端 model.RoomHash 一致：sha256(roomKey) 的前 12 位 hex。 */
        static String roomHash(String roomKey) {
            try {
                MessageDigest md = MessageDigest.getInstance("SHA-256");
                byte[] sum = md.digest(roomKey.getBytes("UTF-8"));
                StringBuilder sb = new StringBuilder();
                for (byte b : sum) {
                    sb.append(String.format("%02x", b));
                }
                return sb.substring(0, 12);
            } catch (Exception e) {
                return "";
            }
        }
    }
}
