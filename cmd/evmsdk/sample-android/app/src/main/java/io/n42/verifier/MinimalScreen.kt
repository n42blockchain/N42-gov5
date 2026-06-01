// SPDX-License-Identifier: LGPL-3.0-or-later
//
// MinimalScreen — Compose UI for the "minimal node": zero-trust tip-following with
// ~1 day of rolling data + trustless account balances. Dark, monospace-numeric,
// trust-flow themed (mirrors the iOS MinimalView).

package io.n42.verifier

import androidx.compose.animation.core.*
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch

private val BG = Color(0xFF0A0D12)
private val GREEN = Color(0xFF33E68C)
private val CYAN = Color(0xFF35D6E6)
private val DIM = Color(0x73FFFFFF)

@Composable
fun MinimalScreen(node: MinimalClient) {
    val st by node.state.collectAsState()
    val accounts by node.accounts.collectAsState()
    val scope = rememberCoroutineScope()

    var idc by remember { mutableStateOf("http://10.0.2.2:8555") }
    var cpBlock by remember { mutableStateOf("990000") }
    var cpHash by remember { mutableStateOf("0x31568223eb0aff7ed01b2ef77d3c61b051ef00781032e00537a7a17176d5e67d") }
    var addr by remember { mutableStateOf("0xdAC17F958D2ee523a2206206994597C13D831ec7") }

    Column(
        Modifier.fillMaxSize().background(BG).verticalScroll(rememberScrollState()).padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(20.dp)
    ) {
        // Header
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.Top) {
            Column(Modifier.weight(1f)) {
                Text("N42 · MINIMAL", color = Color.White, fontSize = 22.sp, fontWeight = FontWeight.Black)
                Text(st.status, color = DIM, fontSize = 12.sp, fontFamily = FontFamily.Monospace)
            }
            AssistChip(onClick = {}, label = { Text("ZERO-TRUST", color = GREEN, fontSize = 11.sp, fontWeight = FontWeight.Bold) },
                colors = AssistChipDefaults.assistChipColors(containerColor = GREEN.copy(alpha = 0.12f)))
        }

        // Chain strip
        Card(GREEN) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text("HEAD ${st.head}", color = GREEN, fontSize = 13.sp, fontFamily = FontFamily.Monospace, fontWeight = FontWeight.SemiBold)
                Text(shortHash(st.headHash), color = DIM, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
            }
            Spacer(Modifier.height(8.dp))
            ChainFlow(active = st.syncing)
            Spacer(Modifier.height(8.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text("⚓ trust root ${st.trustBlock}", color = CYAN, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
                Text("retained ${st.retained} · re-anchors ${st.reanchors}", color = DIM, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
            }
            if (st.syncing) {
                Spacer(Modifier.height(8.dp))
                LinearProgressIndicator(progress = { st.syncProgress }, color = GREEN, modifier = Modifier.fillMaxWidth())
            }
        }

        // Sync card
        Card(CYAN) {
            Field("IDC URL", idc) { idc = it }
            Spacer(Modifier.height(8.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Box(Modifier.weight(0.4f)) { Field("checkpoint #", cpBlock) { cpBlock = it } }
                Box(Modifier.weight(0.6f)) { Field("checkpoint hash", cpHash) { cpHash = it } }
            }
            Spacer(Modifier.height(12.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                FilledTonalButton(onClick = { node.initialize(idc, cpBlock.toLongOrNull() ?: 0, cpHash) }) {
                    Text(if (st.initialized) "Re-anchor" else "Connect")
                }
                FilledTonalButton(
                    onClick = { scope.launch { node.sync() } },
                    enabled = st.initialized && !st.syncing,
                    colors = ButtonDefaults.filledTonalButtonColors(containerColor = GREEN.copy(alpha = 0.18f), contentColor = GREEN)
                ) { Text(if (st.syncing) "Syncing…" else "Follow tip ▶") }
            }
        }

        // Accounts
        Card(GREEN) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.weight(1f)) { Field("account 0x…", addr) { addr = it } }
                Spacer(Modifier.width(8.dp))
                FilledTonalButton(onClick = { node.verifyBalance(addr) }, enabled = st.initialized) { Text("Verify ③") }
            }
            accounts.forEach { a ->
                Spacer(Modifier.height(10.dp))
                AccountRow(a)
            }
        }

        // Resource footer
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceEvenly) {
            Gauge("RAM", "~80 MB", GREEN)
            Gauge("SSD", "~1 day", CYAN)
            Gauge("③ proof", if (st.lastProofBytes > 0) "${st.lastProofBytes} B" else "—", Color(0xFFFFA033))
        }
    }
}

@Composable
private fun ChainFlow(active: Boolean) {
    val t = rememberInfiniteTransition(label = "flow")
    val a by t.animateFloat(0.4f, 0.95f, infiniteRepeatable(tween(800), RepeatMode.Reverse), label = "a")
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("⚓", color = CYAN, fontSize = 12.sp)
        repeat(20) { i ->
            Box(
                Modifier.width(10.dp).height(22.dp)
                    .background(
                        if (i < 16) GREEN.copy(alpha = if (active) a else 0.8f) else Color.White.copy(alpha = 0.10f),
                        RoundedCornerShape(2.dp)
                    )
            )
        }
        Text("→", color = DIM, fontSize = 11.sp)
    }
}

@Composable
private fun AccountRow(a: MinimalClient.Account) {
    Row(
        Modifier.fillMaxWidth().background(Color(0x40000000), RoundedCornerShape(12.dp)).padding(12.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column(Modifier.weight(1f)) {
            Text(shortHash(a.address), color = Color.White, fontSize = 13.sp, fontFamily = FontFamily.Monospace, fontWeight = FontWeight.SemiBold)
            Text(
                if (a.exists) "${weiToEth(a.balanceWei)} ETH · nonce ${a.nonce}" else "absent @ ${a.block}",
                color = DIM, fontSize = 12.sp, fontFamily = FontFamily.Monospace
            )
        }
        Column(horizontalAlignment = Alignment.End) {
            Text(if (a.verified) "🛡" else "⚠", fontSize = 16.sp)
            Text("${a.proofBytes} B", color = DIM, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
        }
    }
}

@Composable
private fun Card(accent: Color, content: @Composable ColumnScope.() -> Unit) {
    Column(
        Modifier.fillMaxWidth().background(Color(0x0AFFFFFF), RoundedCornerShape(16.dp)).padding(16.dp),
        content = content
    )
}

@Composable
private fun Field(ph: String, value: String, onChange: (String) -> Unit) {
    OutlinedTextField(
        value = value, onValueChange = onChange, singleLine = true,
        placeholder = { Text(ph, color = DIM, fontSize = 12.sp) },
        textStyle = androidx.compose.ui.text.TextStyle(fontFamily = FontFamily.Monospace, fontSize = 12.sp, color = Color.White),
        keyboardOptions = KeyboardOptions.Default,
        modifier = Modifier.fillMaxWidth()
    )
}

@Composable
private fun Gauge(k: String, v: String, c: Color) {
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        Text(v, color = c, fontSize = 14.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace)
        Text(k, color = DIM, fontSize = 10.sp)
    }
}

private fun shortHash(s: String): String =
    if (s.length > 14) "${s.take(8)}…${s.takeLast(4)}" else s

private fun weiToEth(wei: String): String =
    (wei.toDoubleOrNull()?.div(1e18))?.let { String.format("%.4f", it) } ?: wei
