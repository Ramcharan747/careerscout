import json
import urllib.request
import urllib.error
import zipfile
import os
import re

starter_urls = [
    "https://www.greenhouse.com/careers", "https://careers.airbnb.com", "https://jobs.lever.co/figma", "https://boards.greenhouse.io/notion", "https://boards.greenhouse.io/airtable", "https://boards.greenhouse.io/brex", "https://boards.greenhouse.io/robinhood", "https://boards.greenhouse.io/plaid", "https://boards.greenhouse.io/gusto", "https://boards.greenhouse.io/gitlab",
    "https://jobs.lever.co/netflix", "https://jobs.lever.co/reddit", "https://jobs.lever.co/coinbase", "https://jobs.lever.co/carta", "https://jobs.lever.co/benchling", "https://jobs.lever.co/scale", "https://jobs.lever.co/rippling", "https://jobs.lever.co/lattice", "https://jobs.lever.co/andela", "https://jobs.lever.co/remote",
    "https://jobs.ashbyhq.com/anthropic", "https://jobs.ashbyhq.com/linear", "https://jobs.ashbyhq.com/vercel", "https://jobs.ashbyhq.com/retool", "https://jobs.ashbyhq.com/ramp", "https://jobs.ashbyhq.com/perplexity", "https://jobs.ashbyhq.com/cursor", "https://jobs.ashbyhq.com/mistral", "https://jobs.ashbyhq.com/arc", "https://jobs.ashbyhq.com/runway",
    "https://careers.microsoft.com", "https://www.amazon.jobs", "https://careers.google.com", "https://jobs.apple.com", "https://www.metacareers.com", "https://www.nvidia.com/en-us/about-nvidia/careers", "https://careers.salesforce.com", "https://careers.adobe.com", "https://jobs.netflix.com", "https://stripe.com/jobs",
    "https://careers.swiggy.com", "https://www.zomato.com/careers", "https://careers.flipkart.com", "https://www.meesho.io/careers", "https://www.ola.money/careers", "https://careers.razorpay.com", "https://www.freshworks.com/company/careers", "https://careers.zoho.com", "https://www.infosys.com/careers", "https://www.wipro.com/careers",
    "https://careers.stripe.com", "https://www.paypal.com/us/webapps/mpp/jobs", "https://careers.revolut.com", "https://www.wise.com/gb/about/careers", "https://careers.klarna.com", "https://www.chime.com/about/careers", "https://careers.braintreepayments.com", "https://www.affirm.com/careers", "https://www.marqeta.com/company/careers", "https://www.adyen.com/careers",
    "https://www.practo.com/company/careers", "https://careers.humana.com", "https://jobs.cvs.com", "https://careers.unitedhealth.com", "https://www.philips.com/a-w/careers", "https://careers.abbott.com", "https://www.healthifyme.com/careers", "https://www.1mg.com/careers", "https://careers.siemens-healthineers.com", "https://www.netmeds.com/careers",
    "https://www.amazon.jobs/en/teams/amazon-india", "https://careers.myntra.com", "https://www.nykaa.com/careers", "https://careers.snapdeal.com", "https://www.paytm.com/care/careers", "https://www.bigbasket.com/careers", "https://corporate.walmart.com/careers", "https://careers.target.com", "https://www.ikea.com/global/en/careers", "https://careers.shopify.com",
    "https://www.lyft.com/careers", "https://www.uber.com/us/en/careers", "https://www.grab.com/sg/careers", "https://careers.gojek.com", "https://www.deliveroo.com/careers", "https://www.doordash.com/careers", "https://www.instacart.com/careers", "https://careers.lalamove.com", "https://www.blinkit.com/careers", "https://careers.dunzo.com",
    "https://www.servicenow.com/careers.html", "https://careers.workday.com", "https://www.sap.com/about/careers.html", "https://careers.oracle.com", "https://www.zendesk.com/jobs", "https://www.hubspot.com/careers", "https://www.twilio.com/company/jobs", "https://www.datadog.com/careers", "https://www.hashicorp.com/careers", "https://jobs.elastic.co"
]

fortune_500_names = [
    "walmart", "amazon", "apple", "cvshealth", "unitedhealthgroup", "exxonmobil", "berkshirehathaway", "alphabet", "mckesson", "chevron",
    "amerisourcebergen", "costco", "microsof", "cardinalhealth", "cigna", "marathonpetroleum", "phillips66", "valeroenergy", "ford", "homedepot",
    "gm", "elevancehealth", "jpmorganchase", "kroger", "centene", "verizon", "walgreens", "fanniemae", "comcast", "att",
    "bankofamerica", "albertsons", "target", "dell", "archer-daniels-midland", "citigroup", "unitedparcel", "pfizer", "lowes", "jnj",
    "fedex", "humana", "energytransfer", "statefarm", "fanniemae", "freddiemac", "pepsico", "wellsfargo", "disney", "intel",
    "lockheedmartin", "caterpillar", "proctergamble", "bunge", "albertsons", "valero", "boeing", "marathon", "sysco", "rtx",
    "morganstanley", "hcahealthcare", "abbvie", "dow", "teslamotors", "aig", "allstate", "exelon", "cisco", "charter",
    "tyson", "newyorklife", "nationwide", "tiaa", "deere", "libertymutual", "coke", "generaldynamics", "chs", "americanexpress",
    "honeywell", "oracle", "usaa", "dukeenergy", "southern", "massmutual", "progressive", "nike", "nxp", "broadcom", "qualcomm",
    "capitalone", "northropgrumman", "3m", "bakerhughes", "halliburton", "abbott", "stryker", "medtronic", "bostonscientific", "thermofisher"
]

all_urls = set()
for url in starter_urls:
    all_urls.add(url)

print(f"Starter URL count: {len(all_urls)}")

# 1. Fetch awesome-career-pages
try:
    print("Fetching awesome-career-pages Portal.json...")
    req = urllib.request.Request("https://raw.githubusercontent.com/CSwala/awesome-career-pages/main/Portal.json")
    with urllib.request.urlopen(req) as response:
        data = json.loads(response.read().decode())
        for item in data:
            if 'Link' in item and item['Link']:
                all_urls.add(item['Link'])
except Exception as e:
    print(f"Failed to fetch awesome-career-pages: {e}")

print(f"URL count after awesome-career-pages: {len(all_urls)}")

# 2. Fetch remote-jobs
try:
    print("Fetching remote-jobs repo...")
    zip_url = "https://github.com/remoteintech/remote-jobs/archive/refs/heads/main.zip"
    zip_path = "/tmp/remote-jobs.zip"
    urllib.request.urlretrieve(zip_url, zip_path)
    
    with zipfile.ZipFile(zip_path, 'r') as zip_ref:
        for file_info in zip_ref.infolist():
            if file_info.filename.endswith('.md') and 'src/companies/' in file_info.filename:
                content = zip_ref.read(file_info.filename).decode('utf-8')
                match = re.search(r'careers_url:\s*(https?://\S+)', content)
                if match:
                    all_urls.add(match.group(1).strip("'\" "))
except Exception as e:
    print(f"Failed to fetch remote-jobs: {e}")

print(f"URL count after remote-jobs: {len(all_urls)}")

# 3. Add Fortune 500 up to 1000
missing = 1000 - len(all_urls)
if missing > 0:
    print(f"Adding {missing} Fortune 500 combinations...")
    for i, name in enumerate(fortune_500_names):
        if len(all_urls) >= 1000:
            break
        all_urls.add(f"https://www.{name}.com/careers")
        if len(all_urls) < 1000:
            all_urls.add(f"https://careers.{name}.com")

print(f"Final URL count: {len(all_urls)}")

urls_list = list(all_urls)[:1000]

with open('careers_urls.json', 'w') as f:
    json.dump(urls_list, f, indent=2)

print(f"Successfully saved {len(urls_list)} URLs to careers_urls.json")
